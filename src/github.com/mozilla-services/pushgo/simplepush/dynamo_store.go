/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/goamz/goamz/aws"
	"github.com/goamz/goamz/dynamodb"
)

const (
	DB_LABEL   = "dynamodb"
	UAID_LABEL = "uaid"
	CHID_LABEL = "chid"
	PING_LABEL = "proprietary_ping"
	VERS_LABEL = "version"
	MODD_LABEL = "modified"
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
	pk            dynamodb.PrimaryKey
	server        dynamoServer
	table         dynamoTable
	tablename     string
	readProv      int64
	writeProv     int64
	closeSignal   chan bool
	maxChannels   int
	statusTimeout time.Duration
	statusIdle    time.Duration
}

func (s *DynamoDBStore) waitUntilStatus(table dynamoTable, status string) (err error) {
	done := make(chan bool)
	timeout := time.After(s.statusTimeout)
	defer func() {
		if r := recover(); r != nil {
			if err, ok := r.(error); ok {
				if s.logger.ShouldLog(ERROR) {
					s.logger.Error("dynamodb", "Recovering socket issue",
						LogFields{"error": err.Error()})
				}
			}
		}
	}()
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				desc, err := table.DescribeTable()
				if err != nil {
					return
				}
				if desc.TableStatus == status {
					done <- true
					return
				}
				time.Sleep(s.statusIdle)
			}
		}
	}()
	select {
	case <-done:
		err = nil
	case <-timeout:
		err = ErrDynamoDBTimeout
		close(done)
	}
	return
}

func (s *DynamoDBStore) createTable() (err error) {
	/* There are three primary fields for each row,
	   uaid = UserAgentID, all registered items must have one of these
	   chid = Either the ChannelID or " ". In this case " " fills the
	   		"main" UAID entry, which can carry info like the Proprietary
	   		Ping data, etc. CHID rows can be accessed by specifing a
	   		QUERY with chid > " ".
	   created = UTC().Unix() of the record creation. Used for cleanup.
	*/
	tableDesc := dynamodb.TableDescriptionT{
		TableName: s.tablename,
		AttributeDefinitions: []dynamodb.AttributeDefinitionT{
			dynamodb.AttributeDefinitionT{UAID_LABEL, "S"},
			dynamodb.AttributeDefinitionT{CHID_LABEL, "S"},
			dynamodb.AttributeDefinitionT{MODD_LABEL, "N"},
		},
		KeySchema: []dynamodb.KeySchemaT{
			dynamodb.KeySchemaT{UAID_LABEL, "HASH"},
			dynamodb.KeySchemaT{CHID_LABEL, "RANGE"},
		},
		GlobalSecondaryIndexes: []dynamodb.GlobalSecondaryIndexT{
			dynamodb.GlobalSecondaryIndexT{
				IndexName: "uaid-modified-index",
				KeySchema: []dynamodb.KeySchemaT{
					dynamodb.KeySchemaT{UAID_LABEL, "HASH"},
					dynamodb.KeySchemaT{MODD_LABEL, "RANGE"},
				},
				Projection: dynamodb.ProjectionT{"ALL"},
				ProvisionedThroughput: dynamodb.ProvisionedThroughputT{
					ReadCapacityUnits:  s.readProv,
					WriteCapacityUnits: s.writeProv,
				},
			},
		},
		ProvisionedThroughput: dynamodb.ProvisionedThroughputT{
			ReadCapacityUnits:  s.readProv,
			WriteCapacityUnits: s.writeProv,
		},
	}

	pk, err := tableDesc.BuildPrimaryKey()
	if err != nil {
		s.logger.Panic(DB_LABEL, "Could not create primary key",
			LogFields{"error": err.Error()})
		return
	}
	s.table = s.server.NewTable(tableDesc.TableName, pk)
	_, err = s.server.CreateTable(tableDesc)
	if err != nil {
		s.logger.Panic(DB_LABEL, "Could not create",
			LogFields{"error": err.Error()})
		return
	}

	err = s.waitUntilStatus(s.table, "ACTIVE")
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
	tables, err := s.server.ListTables()
	if err != nil {
		s.logger.Panic("dynamodb", "Could not query dynamodb",
			LogFields{"error": err.Error()})
		return
	}
	s.readProv = conf.ReadProv
	s.writeProv = conf.WriteProv
	s.maxChannels = conf.MaxChannels
	s.tablename = conf.TableName

	// check if the table exists
	if !ddb_containsTable(tables, conf.TableName) {
		if s.logger.ShouldLog(INFO) {
			s.logger.Info("dynamodb", "creating dynamodb table...", nil)
		}
		if err = s.createTable(); err != nil {
			s.logger.Panic("dynamodb", "Could not create table",
				LogFields{"error": err.Error()})
			return
		}
	}
	s.pk = dynamodb.PrimaryKey{dynamodb.NewStringAttribute(UAID_LABEL, ""),
		dynamodb.NewStringAttribute(CHID_LABEL, "")}
	s.table = s.server.NewTable(conf.TableName, s.pk)
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
	success = false
	tables, err := s.server.ListTables()
	success = err == nil && ddb_containsTable(tables, s.tablename)
	return true, nil
}

// Exists returns a Boolean indicating whether a device has previously
// registered with the Simple Push server. Implements Store.Exists().
func (s *DynamoDBStore) Exists(uaid string) bool {
	res, err := s.table.CountQuery([]dynamodb.AttributeComparison{
		*dynamodb.NewEqualStringAttributeComparison(UAID_LABEL, uaid),
		*dynamodb.NewEqualStringAttributeComparison(CHID_LABEL, " "),
	})
	if err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error(DB_LABEL, "Exists failed",
				LogFields{"error": err.Error()})
		}
		return false
	}
	return res > 0
}

// Register creates and stores a channel record for the given device ID and
// channel ID. If version > 0, the record will be marked as active. Implements
// Store.Register().
func (s *DynamoDBStore) Register(uaid, chid string, version int64) (err error) {
	// try to put the master record
	now := ddb_getNow(0)
	vers := strconv.FormatInt(version, 10)
	success, err := s.table.PutItem(uaid, " ", []dynamodb.Attribute{
		*dynamodb.NewNumericAttribute(MODD_LABEL, now),
	})
	if err != nil {
		return
	}
	// now add the chid.
	success, err = s.table.PutItem(uaid, chid, []dynamodb.Attribute{
		*dynamodb.NewNumericAttribute(VERS_LABEL, vers),
		*dynamodb.NewNumericAttribute(MODD_LABEL, now),
	})
	if err != nil {
		return
	}
	if success != true {
		err = ErrDynamoDBFailure
	}
	return
}

// Update updates the version for the given device ID and channel ID.
// Implements Store.Update().
func (s *DynamoDBStore) Update(uaid, chid string, version int64) (err error) {
	now := ddb_getNow(0)
	// Write or update the existing value.
	// NOTE: ConditionalUpdateAttributes requires knowledge of the previous
	// value (it only updates if the previous value matches)
	// this is a problem for new fields or running statelessly.
	// It does mean that there's a chance of a delayed packet decrementing
	// a version or otherwise causing an issue.
	// This can either be resolved by pre-fetching existing data and only
	// updating if the version is greater, (thus doubling the dynamodb access
	// rate or by just accepting that it's a possiblity and dealing with it
	// later if it's a problem.
	success, err := s.table.UpdateAttributes(
		&dynamodb.Key{uaid, chid},
		[]dynamodb.Attribute{
			*dynamodb.NewNumericAttribute(VERS_LABEL,
				strconv.FormatInt(version, 10)),
			*dynamodb.NewNumericAttribute(MODD_LABEL, now),
		},
	)
	if err != nil {
		return
	}
	if success != true {
		err = ErrDynamoDBFailure
	}
	return
}

// Unregister marks the channel ID associated with the given device ID
// as inactive. Implements Store.Unregister().
func (s *DynamoDBStore) Unregister(uaid, chid string) (err error) {
	success, err := s.table.DeleteItem(&dynamodb.Key{uaid, chid})
	if err != nil {
		return
	}
	if success != true {
		err = ErrDynamoDBFailure
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

	attrs := []dynamodb.AttributeComparison{
		*dynamodb.NewEqualStringAttributeComparison(UAID_LABEL, uaid),
		*dynamodb.NewStringAttributeComparison(CHID_LABEL, dynamodb.COMPARISON_GREATER_THAN, " "),
	}
	results, err := s.table.Query(
		attrs,
	)
	if err != nil {
		return
	}
	for _, r := range results {
		var vers string
		if vera, ok := r[VERS_LABEL]; ok {
			vers = vera.Value
		} else {
			if vera, ok := r["created"]; ok {
				vers = vera.Value
			} else {
				vers = ddb_getNow(0)
			}
		}
		version, err := strconv.ParseUint(vers, 10, 64)
		if err != nil {
			version = uint64(time.Now().UTC().Unix())
		}
		updates = append(updates, Update{
			ChannelID: r[CHID_LABEL].Value,
			Version:   version,
		})
	}

	return
}

// DropAll removes all channel records for the given device ID. Implements
// Store.DropAll().
func (s *DynamoDBStore) DropAll(uaid string) (err error) {
	//TODO: Pass this to a goroutine to make it non blocking.
	result, err := s.table.Query([]dynamodb.AttributeComparison{
		*dynamodb.NewEqualStringAttributeComparison(UAID_LABEL, uaid),
	})
	if err != nil {
		return
	}
	// Need to reflect to prevent testing from causing panics galore.
	// Damn good thing this method is not on the critical path.
	testing := reflect.TypeOf(s.table).String() == "*simplepush.testDynamoTable"
	items := make(map[string][][]dynamodb.Attribute)
	items["Delete"] = make([][]dynamodb.Attribute, len(result))
	for n, record := range result {
		//Use partial names for item labels (e.g. Put or Delete instead of
		// "PutRequest" or "DeleteRequest")
		items["Delete"][n] = []dynamodb.Attribute{
			dynamodb.Attribute{
				Type:  dynamodb.TYPE_STRING,
				Name:  UAID_LABEL,
				Value: record[UAID_LABEL].Value,
			},
			dynamodb.Attribute{
				Type:  dynamodb.TYPE_STRING,
				Name:  CHID_LABEL,
				Value: record[CHID_LABEL].Value,
			}}
	}
	defer func() {
		// unravelling this is.. not simple or well explained.
		// added bonus that there's no clear example of how to do it.
		// So this will probably puke or throw exceptions like an angry
		// monkey.
		if r := recover(); r != nil {
			if err, ok := r.(error); ok {
				if s.logger.ShouldLog(ERROR) {
					s.logger.Error("dynamodb", "Unhandled exception during dropall",
						LogFields{"error": err.Error()})
				}
			}
		}
	}()
	var i int
	for i = 0; i < 5; i++ {
		//AMZ states that it's possible for a BatchWrite to partially fail.
		// In that case, you're required to retry the "missed" records after
		// some period, ideally, with an exponential timeout
		// I'm also arbitrarily giving up after 5 retries to prevent endless
		// loops if crap has really gone to hell.
		batch := s.table.BatchWriteItems(items)

		missed := make(map[string]interface{})
		if !testing {
			missed, err = batch.Execute()
		}
		if err != nil {
			if s.logger.ShouldLog(ERROR) {
				s.logger.Error("dynamodb", "Could not complete DropAll",
					LogFields{"error": err.Error()})
			}
			return err
		}
		if missActions, ok := missed["DeleteRequest"]; ok {

			// This may throw an index errorfor various reasons.
			items = missActions.(dynamodb.BatchWriteItem).ItemActions[s.table.(*dynamodb.Table)]
			if len(items) > 0 {
				// TODO: replace this with a proper exponential backoff.
				time.Sleep(time.Duration(rand.Intn(4)+1) * time.Second)
				continue
			}
		}
		break
	}
	if i == 4 {
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("dynamodb", "Could not drop all records for uaid",
				LogFields{"uaid": uaid})
		}
	}
	return nil
}

// FetchPing retrieves proprietary ping information for the given device ID
// from storage. Implements Store.FetchPing().
func (s *DynamoDBStore) FetchPing(uaid string) (pingData []byte, err error) {
	result, err := s.table.Query([]dynamodb.AttributeComparison{
		*dynamodb.NewEqualStringAttributeComparison(UAID_LABEL, uaid),
		*dynamodb.NewEqualStringAttributeComparison(CHID_LABEL, " "),
	})
	if err != nil {
		return
	}
	if len(result) > 0 {
		if pp, ok := result[0][PING_LABEL]; ok {
			pingData = []byte(pp.Value)
		}
	}
	return
}

// PutPing stores the proprietary ping info blob for the given device ID in
// storage. Implements Store.PutPing().
func (s *DynamoDBStore) PutPing(uaid string, pingData []byte) (err error) {
	// s.table.
	_, err = s.table.UpdateAttributes(&dynamodb.Key{uaid, " "},
		[]dynamodb.Attribute{
			*dynamodb.NewStringAttribute(PING_LABEL, string(pingData)),
		},
	)
	return
}

// DropPing removes all proprietary ping info for the given device ID.
// Implements Store.DropPing().
func (s *DynamoDBStore) DropPing(uaid string) (err error) {
	_, err = s.table.DeleteAttributes(&dynamodb.Key{uaid, " "},
		[]dynamodb.Attribute{
			*dynamodb.NewStringAttribute(PING_LABEL, ""),
		},
	)
	return
}

func init() {
	AvailableStores["dynamodb"] = func() HasConfigStruct {
		return NewDynamoDB()
	}
}
