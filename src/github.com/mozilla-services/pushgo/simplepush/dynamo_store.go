/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/goamz/goamz/aws"
	"github.com/mozilla-services/pushgo/dynamodb"
)

const (
	DB_LABEL      = "dynamodb"
	UAID_LABEL    = "uaid"
	CHID_LABEL    = "chid"
	PING_LABEL    = "proprietary_ping"
	VERS_LABEL    = "version"
	MODF_LABEL    = "modified"
	CREA_LABEL    = "created"
	AWS_MAX_BATCH = 25
)

var (
	ErrDynamoDBInvalidRegion = errors.New("DynamoDB: Invalid region")
	ErrDynamoDBBatchFailure  = errors.New("DynamoDB: Too many items to batch")
	Now                      = dynamodb.Now
)

type DynamoDBConf struct {
	// Name of the table to use
	TableName string `toml:"tablename" env:"tablename"`

	// number of provisioned hosts (more hosts = more money)
	ReadProv  int64 `toml:"read_cap_units" env:"read_cap_units"`
	WriteProv int64 `toml:"write_cap_units" env:"write_cap_units"`

	// Region that the dbstore is in
	Region string `toml:"aws_region" env:"aws_region"`

	// Auth info (using aws.GetAuth)
	// NOTE: if using environment variables, please use goamz Var names.
	// AWS_ACCESS_KEY
	// AWS_SECRET_KEY
	Access string `toml:"aws_access_key" env:"aws_access_key"`
	Secret string `toml:"aws_access_secret" env:"aws_access_secret"`

	MaxChannels int `toml:"max_channels" env:"max_channels"`
}

type DynamoDBStore struct {
	logger        *SimpleLogger
	region        aws.Region
	auth          aws.Auth
	server        dynamodb.DynamoServer
	table         dynamodb.DynamoTable
	tableName     string
	readProv      int64
	writeProv     int64
	closeSignal   chan bool
	maxChannels   int
	statusTimeout time.Duration
	statusIdle    time.Duration
}

func (s *DynamoDBStore) createTable(server dynamodb.DynamoServer) (err error) {
	/* There are three primary fields for each row,
	   uaid = UserAgentID, all registered items must have one of these
	   chid = Either the ChannelID or " ". In this case " " fills the
	   		"main" UAID entry, which can carry info like the Proprietary
	   		Ping data, etc. CHID rows can be accessed by specifing a
	   		QUERY with chid > " ".
	   created = UTC().Unix() of the record creation. Used for cleanup.
	*/
	s.table = &dynamodb.Table{
		TableName: s.tableName,
		AttributeDefinitions: []dynamodb.AttributeDefinition{
			dynamodb.AttributeDefinition{UAID_LABEL, "S"},
			dynamodb.AttributeDefinition{CHID_LABEL, "S"},
			dynamodb.AttributeDefinition{MODF_LABEL, "N"},
		},
		KeySchema: []dynamodb.KeySchema{
			dynamodb.KeySchema{UAID_LABEL, "HASH"},
			dynamodb.KeySchema{CHID_LABEL, "RANGE"},
		},
		GlobalSecondaryIndexes: []dynamodb.SecondaryIndex{
			dynamodb.SecondaryIndex{
				IndexName: "uaid-modified-index",
				KeySchema: []dynamodb.KeySchema{
					dynamodb.KeySchema{UAID_LABEL, "HASH"},
					dynamodb.KeySchema{MODF_LABEL, "RANGE"},
				},
				Projection: dynamodb.Projection{
					NonKeyAttributes: []string{},
					ProjectionType:   "ALL",
				},
				ProvisionedThroughput: dynamodb.ProvisionedThroughput{
					ReadCapacityUnits:  s.readProv,
					WriteCapacityUnits: s.writeProv,
				},
			},
		},
		ProvisionedThroughput: dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  s.readProv,
			WriteCapacityUnits: s.writeProv,
		},
		Server: server,
	}

	// Table creation almost always requires at least two reads
	_, err = s.table.Create()
	if err != nil {
		s.logger.Panic(DB_LABEL, "Could not create",
			LogFields{"error": err.Error()})
		return
	}

	err = s.table.WaitUntilStatus("ACTIVE", s.statusIdle, s.statusTimeout)
	if err != nil {
		s.logger.Panic("dynamo", "Could not file table.",
			LogFields{"error": err.Error()})
		return
	}
	return
}

func NewDynamoDB() *DynamoDBStore {
	s := &DynamoDBStore{
		statusTimeout: 3 * time.Minute,
		statusIdle:    5 * time.Second,
	}
	return s
}

func (*DynamoDBStore) ConfigStruct() interface{} {
	return &DynamoDBConf{
		MaxChannels: 200,
		TableName:   "simplepush",
		Region:      "us-west-1",
		Access:      "MISSING_ACCESS_KEY",
		Secret:      "MISSING_SECRET_KEY",
		ReadProv:    1,
		WriteProv:   1,
	}
}

// Implements HasConfigStruct.Init().
func (s *DynamoDBStore) Init(app *Application, config interface{}) (err error) {
	var ok bool
	conf := config.(*DynamoDBConf)
	s.logger = app.Logger()

	// create the server
	s.region, ok = aws.Regions[conf.Region]
	if !ok {
		s.logger.Panic("dynamodb", "Invalid region specified", nil)
		return ErrDynamoDBInvalidRegion
	}
	auth, err := aws.GetAuth(conf.Access, conf.Secret,
		"", time.Now().Add(s.statusTimeout))
	if err != nil {
		s.logger.Panic("dynamodb", "Could not log into dynamodb",
			LogFields{"error": err.Error()})
		return
	}

	if s.server == nil {
		s.server = &dynamodb.Server{auth, s.region}
	}

	s.readProv = conf.ReadProv
	s.writeProv = conf.WriteProv
	s.maxChannels = conf.MaxChannels
	s.tableName = conf.TableName
	s.table, err = dynamodb.DescribeTable(s.server, conf.TableName)
	if err != nil {
		s.logger.Panic("dynamodb", "decribe table",
			LogFields{"error": err.Error()})
		// create Table here
		if s.logger.ShouldLog(INFO) {
			s.logger.Info("dynamodb", "creating dynamodb table...", nil)
		}
		if err = s.createTable(s.server); err != nil {
			s.logger.Panic("dynamodb", "Could not create table",
				LogFields{"error": err.Error()})
			return
		}
	}
	return nil
}

// CanStore indicates whether the specified number of channel registrations
// are allowed per client. Implements Store.CanStore().
func (s *DynamoDBStore) CanStore(channels int) bool {
	return channels <= s.maxChannels
}

// Close closes the connection pool and unblocks all pending operations with
// errors. Safe to call multiple times. Implements Store.Close().
func (s *DynamoDBStore) Close() (err error) {
	return
}

// KeyToIDs extracts the hex-encoded device and channel IDs from a user-
// readable primary key. Implements Store.KeyToIDs().
func (*DynamoDBStore) KeyToIDs(key string) (uaid, chid string, err error) {
	items := strings.SplitN(key, ".", 2)
	if len(items) < 2 {
		return "", "", nil
	}
	return items[0], items[1], nil
}

// IDsToKey generates a user-readable primary key from a (device ID, channel
// ID) tuple. The primary key is encoded in the push endpoint URI. Implements
// Store.IDsToKey().
func (*DynamoDBStore) IDsToKey(uaid, chid string) (string, error) {
	if len(uaid) == 0 || len(chid) == 0 {
		return "", nil
	}
	return uaid + "." + chid, nil
}

// Implements Store.Status().
func (s *DynamoDBStore) Status() (success bool, err error) {
	tables, _, err := dynamodb.ListTables(s.server, "")
	if err != nil {
		return false, err
	}
	if dynamodb.SetContains(tables, s.tableName) {
		return true, nil
	}
	if s.logger.ShouldLog(ERROR) {
		s.logger.Error(DB_LABEL, "Can't find table in dynamodb store", nil)
	}

	return false, nil
}

// Exists returns a Boolean indicating whether a device has previously
// registered with the Simple Push server. Implements Store.Exists().
func (s *DynamoDBStore) Exists(uaid string) bool {
	res, err := s.table.Query(&dynamodb.ItemQuery{
		KeyConditions: map[string]dynamodb.KeyCondition{
			UAID_LABEL: dynamodb.KeyCondition{
				AttributeValueList: []dynamodb.Attribute{
					dynamodb.NewAttribute("S", uaid),
				},
				ComparisonOperator: "EQ"},
			CHID_LABEL: dynamodb.KeyCondition{
				AttributeValueList: []dynamodb.Attribute{
					dynamodb.NewAttribute("S", " "),
				},
				ComparisonOperator: "EQ"},
		},
	})
	if err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error(DB_LABEL, "Exists failed",
				LogFields{"error": err.Error()})
		}
		return false
	}
	return res.Count > 0
}

// Register creates and stores a channel record for the given device ID and
// channel ID. If version > 0, the record will be marked as active. Implements
// Store.Register().
func (s *DynamoDBStore) Register(uaid, chid string, version int64) (err error) {
	// try to put the master record
	now := Now(0)
	s.table.UpdateItem(&dynamodb.ItemUpdate{
		Key: map[string]dynamodb.Attribute{
			UAID_LABEL: dynamodb.NewAttribute("S", uaid),
			CHID_LABEL: dynamodb.NewAttribute("S", " "),
		},
		ExpressionAttributeNames: map[string]string{
			"#c": CREA_LABEL,
			"#m": MODF_LABEL,
		},
		ExpressionAttributeValues: map[string]dynamodb.Attribute{
			":c": dynamodb.NewAttribute("N", now),
			":m": dynamodb.NewAttribute("N", now),
		},
		ConditionExpression: "attribute_not_exists(#c)",
		UpdateExpression:    "SET #c=:c, #m=:m",
	})
	// now add the chid.
	_, err = s.table.PutItem(&dynamodb.ItemRequest{
		Item: map[string]dynamodb.Attribute{
			UAID_LABEL: dynamodb.NewAttribute("S", uaid),
			CHID_LABEL: dynamodb.NewAttribute("S", chid),
			VERS_LABEL: dynamodb.NewAttribute("N", version),
			MODF_LABEL: dynamodb.NewAttribute("N", now),
			CREA_LABEL: dynamodb.NewAttribute("N", now),
		},
	})
	if err != nil {
		return
	}
	return
}

// Update updates the version for the given device ID and channel ID.
// Implements Store.Update().
func (s *DynamoDBStore) Update(uaid, chid string, version int64) (err error) {
	// Write or update the existing value.
	_, err = s.table.UpdateItem(&dynamodb.ItemUpdate{
		Key: map[string]dynamodb.Attribute{
			UAID_LABEL: dynamodb.NewAttribute("S", uaid),
			CHID_LABEL: dynamodb.NewAttribute("S", chid),
		},
		ExpressionAttributeNames: map[string]string{
			"#ver": VERS_LABEL,
			"#mod": MODF_LABEL,
		},
		ExpressionAttributeValues: map[string]dynamodb.Attribute{
			":ver": dynamodb.NewAttribute("N", version),
			":mod": dynamodb.NewAttribute("N", Now(0)),
		},
		ConditionExpression: "#ver < :ver",
		UpdateExpression:    "SET #ver=:ver, #mod=:mod",
	})

	if err != nil {
		return
	}
	return
}

// Unregister marks the channel ID associated with the given device ID
// as inactive. Implements Store.Unregister().
func (s *DynamoDBStore) Unregister(uaid, chid string) (err error) {
	_, err = s.table.DeleteItem(&dynamodb.ItemRequest{
		Key: map[string]dynamodb.Attribute{
			UAID_LABEL: dynamodb.NewAttribute("S", uaid),
			CHID_LABEL: dynamodb.NewAttribute("S", chid),
		},
	})
	if err != nil {
		return
	}
	return
}

// Drop removes a channel ID associated with the given device ID from
// storage. Deregistration calls should call s.Unregister() instead.
// Implements Store.Drop().
func (s *DynamoDBStore) Drop(uaid, chid string) (err error) {
	return s.Unregister(uaid, chid)
}

// FetchAll returns all channel updates and expired channels for a device ID
// since the specified cutoff time. Implements Store.FetchAll().
func (s *DynamoDBStore) FetchAll(uaid string, since time.Time) (updates []Update, expired []string, err error) {

	query := &dynamodb.ItemQuery{
		KeyConditions: map[string]dynamodb.KeyCondition{
			UAID_LABEL: dynamodb.KeyCondition{
				[]dynamodb.Attribute{dynamodb.NewAttribute("S", uaid)},
				"EQ"},
			CHID_LABEL: dynamodb.KeyCondition{
				[]dynamodb.Attribute{dynamodb.NewAttribute("S", " ")},
				"GT"},
		},
		/*
			ExpressionAttributeValues: map[string]dynamodb.Attribute{
				":mod": dynamodb.NewAttribute("S", MODF_LABEL),
				":now": dynamodb.NewAttribute("N", time.Now().Sub(since).Seconds()),
			},
			FilterExpression: ":mod > :now",
		*/
	}
	results, err := s.table.Query(query)
	if err != nil {
		return
	}
	for _, r := range results.Items {
		var vers int64
		if vera, ok := r[VERS_LABEL]; ok {
			vers = vera.(map[string]interface{})["N"].(int64)
		} else {
			if vera, ok := r["created"]; ok {
				vers = vera.(map[string]interface{})["N"].(int64)
			} else {
				vers = Now(0)
			}
		}
		version := uint64(vers)
		updates = append(updates, Update{
			ChannelID: r[CHID_LABEL].(map[string]interface{})["S"].(string),
			Version:   version,
		})
	}
	return
}

// DropAll removes all channel records for the given device ID. Implements
// Store.DropAll().
func (s *DynamoDBStore) DropAll(uaid string) (err error) {
	//TODO: Pass this to a goroutine to make it non blocking.
	all, _, err := s.FetchAll(uaid, time.Unix(0, 0))
	if err != nil {
		return
	}

	// AWS maxes out at 25 records per batch.
	listMax := AWS_MAX_BATCH
	if len(all)+1 < listMax {
		listMax = len(all) + 1
	}
	list := make([]dynamodb.BatchWriteRequestItem, listMax)
	// max 25 records at a go.
	for {
		i := 0
		// add up to 25 of the available records to the purge list.
		for _, item := range all {
			if i >= AWS_MAX_BATCH {
				break
			}
			list[i] = dynamodb.BatchWriteRequestItem{
				DeleteRequest: &dynamodb.BatchDelete{
					Key: map[string]dynamodb.Attribute{
						UAID_LABEL: dynamodb.NewAttribute("S", uaid),
						CHID_LABEL: dynamodb.NewAttribute("S", item.ChannelID),
					},
				},
			}
			i++
		}
		// If we have room, add the master record
		if i < AWS_MAX_BATCH {
			list[i] = dynamodb.BatchWriteRequestItem{
				DeleteRequest: &dynamodb.BatchDelete{
					Key: map[string]dynamodb.Attribute{
						UAID_LABEL: dynamodb.NewAttribute("S", uaid),
						CHID_LABEL: dynamodb.NewAttribute("S", " "),
					},
				},
			}
		}

		// Handle any unprocessed resubmits.
		unprocessed := map[string][]dynamodb.BatchWriteRequestItem{
			s.tableName: list,
		}
		loopCount := 5
		for unprocessed != nil {
			reply, err := dynamodb.BatchWriteItem(
				s.server,
				&dynamodb.BatchWriteQuery{
					RequestItems: unprocessed,
				})
			if err != nil {
				return err
			}
			unprocessed = nil
			if len(reply.UnprocessedItems) > 0 {
				_, ok := reply.UnprocessedItems[s.tableName]
				if !ok {
					break
				}
				unprocessed = reply.UnprocessedItems
			}
			loopCount--
			if loopCount < 0 {
				break
			}
		}
		// if we have more pending, handle that batch.
		if len(all) > AWS_MAX_BATCH {
			all = all[AWS_MAX_BATCH:]
			continue
		}
		// otherwise break out, we're done.
		break
	}
	return nil
}

// FetchPing retrieves proprietary ping information for the given device ID
// from storage. Implements Store.FetchPing().
func (s *DynamoDBStore) FetchPing(uaid string) (pingData []byte, err error) {
	result, err := s.table.Query(&dynamodb.ItemQuery{
		KeyConditions: map[string]dynamodb.KeyCondition{
			UAID_LABEL: dynamodb.KeyCondition{
				[]dynamodb.Attribute{dynamodb.NewAttribute("S", uaid)},
				"EQ"},
			CHID_LABEL: dynamodb.KeyCondition{
				[]dynamodb.Attribute{dynamodb.NewAttribute("S", " ")},
				"EQ"},
		},
	})
	if err != nil {
		return
	}
	if result.Count > 0 {
		if pp, ok := result.Items[0][PING_LABEL]; ok {
			pingData, err = base64.StdEncoding.DecodeString(pp.(map[string]interface{})["B"].(string))
		}
	}
	return
}

// PutPing stores the proprietary ping info blob for the given device ID in
// storage. Implements Store.PutPing().
func (s *DynamoDBStore) PutPing(uaid string, pingData []byte) (err error) {
	_, err = s.table.UpdateItem(&dynamodb.ItemUpdate{
		Key: map[string]dynamodb.Attribute{
			UAID_LABEL: dynamodb.NewAttribute("S", uaid),
			CHID_LABEL: dynamodb.NewAttribute("S", " "),
		},
		ExpressionAttributeNames: map[string]string{
			"#ping": PING_LABEL,
			"#modf": MODF_LABEL,
		},
		ExpressionAttributeValues: map[string]dynamodb.Attribute{
			":ping": dynamodb.NewAttribute("B", pingData),
			":modf": dynamodb.NewAttribute("N", Now(0)),
		},
		UpdateExpression: "SET #ping=:ping, #modf=:modf",
		ReturnValues:     "NONE",
	})
	return
}

// DropPing removes all proprietary ping info for the given device ID.
// Implements Store.DropPing().
func (s *DynamoDBStore) DropPing(uaid string) (err error) {
	_, err = s.table.UpdateItem(&dynamodb.ItemUpdate{
		Key: map[string]dynamodb.Attribute{
			UAID_LABEL: dynamodb.NewAttribute("S", uaid),
			CHID_LABEL: dynamodb.NewAttribute("S", " "),
		},
		UpdateExpression: "remove " + PING_LABEL + " = :ping",
	})
	return
}

func init() {
	AvailableStores["dynamodb"] = func() HasConfigStruct {
		return NewDynamoDB()
	}
}
