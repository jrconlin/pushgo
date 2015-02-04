/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jrconlin/goamz/aws"
	"github.com/jrconlin/goamz/dynamodb"
)

const (
	DB_LABEL = "dynamodb"
)

var (
	ErrInvalidRegion   = errors.New("Invalid Region")
	ErrDynamoDBTimeout = errors.New("DynamoDB function timed out")
	ErrDynamoDBFailure = errors.New("DynamoDB returned non success")
)

type DynamoDBConf struct {
	// Name of the table to use
	TableName string `toml:"tablename" env:"tablename"`

	// number of provisioned hosts (more hosts = more money)
	ReadProv  int64 `toml:"read_cap_units" env:"read_cap_units"`
	WriteProv int64 `toml:"write_cap_units" env:"write_cap_units"`

	// Region that the dbstore is in
	Region string `toml:"region" env:"region"`

	// Auth info (using aws.GetAuth)
	// NOTE: if using environment variables, please use goamz Var names.
	// AWS_ACCESS_KEY
	// AWS_SECRET_KEY
	Access string `toml:"accesskey" `
	Secret string `toml:"secret" `

	MaxChannels int `toml:"max_channels" env:"max_channels"`
}

type dynamoTable interface {
	DescribeTable() (*dynamodb.TableDescriptionT, error)
	CountQuery([]dynamodb.AttributeComparison) (int64, error)
	PutItem(string, string, []dynamodb.Attribute) (bool, error)
	UpdateAttributes(*dynamodb.Key, []dynamodb.Attribute) (bool, error)
	DeleteItem(*dynamodb.Key) (bool, error)
	Query([]dynamodb.AttributeComparison) ([]map[string]*dynamodb.Attribute, error)
	DeleteAttributes(*dynamodb.Key, []dynamodb.Attribute) (bool, error)
}

type dynamoServer interface {
	NewTable(string, dynamodb.PrimaryKey) *dynamodb.Table
	CreateTable(dynamodb.TableDescriptionT) (string, error)
	ListTables() ([]string, error)
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

const (
	RFC_ISO_8601  = "20060102T150405Z"
	DYNAMO_PREFIX = "DynamoDB_20120810"
)

func (s *DynamoDBStore) waitUntilStatus(table dynamoTable, status string) (err error) {
	done := make(chan bool)
	timeout := time.After(s.statusTimeout)
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
		break
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
			dynamodb.AttributeDefinitionT{"uaid", "S"},
			dynamodb.AttributeDefinitionT{"chid", "S"},
			dynamodb.AttributeDefinitionT{"created", "N"},
		},
		KeySchema: []dynamodb.KeySchemaT{
			dynamodb.KeySchemaT{"uaid", "HASH"},
			dynamodb.KeySchemaT{"chid", "RANGE"},
		},
		GlobalSecondaryIndexes: []dynamodb.GlobalSecondaryIndexT{
			dynamodb.GlobalSecondaryIndexT{
				IndexName: "chid-created-index",
				KeySchema: []dynamodb.KeySchemaT{
					dynamodb.KeySchemaT{"chid", "HASH"},
					dynamodb.KeySchemaT{"created", "RANGE"},
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

// NewEmcee creates an unconfigured memcached adapter.
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

func (s *DynamoDBStore) containsTable(tableSet []string, table string) bool {
	if len(tableSet) == 0 {
		return false
	}
	for _, t := range tableSet {
		if t == table {
			return true
		}
	}
	return false
}

// Init initializes the memcached adapter with the given configuration.
// Implements HasConfigStruct.Init().
func (s *DynamoDBStore) Init(app *Application, config interface{}) (err error) {
	var ok bool
	conf := config.(*DynamoDBConf)
	s.logger = app.Logger()

	// create the server
	s.region, ok = aws.Regions[conf.Region]
	if !ok {
		s.logger.Panic("dynamodb", "Invalid region specified", nil)
		return ErrInvalidRegion
	}
	auth, err := aws.GetAuth(conf.Access, conf.Secret,
		"", time.Now().Add(s.statusTimeout))
	if err != nil {
		s.logger.Panic("dynamodb", "Could not log into dynamodb",
			LogFields{"error": err.Error()})
		return
	}

	if s.server == nil {
		s.logger.Error("debug", "generating new server", nil)
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
	if !s.containsTable(tables, conf.TableName) {
		if s.logger.ShouldLog(INFO) {
			s.logger.Info("dynamodb", "creating dynamodb table...", nil)
		}
		if err = s.createTable(); err != nil {
			s.logger.Panic("dynamodb", "Could not create table",
				LogFields{"error": err.Error()})
			return
		}
	}
	s.pk = dynamodb.PrimaryKey{dynamodb.NewStringAttribute("uaid", ""),
		dynamodb.NewStringAttribute("chid", "")}
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
	return fmt.Sprintf("%s.%s", uaid, chid), nil
}

// Status queries whether memcached is available for reading and writing.
// Implements Store.Status().
func (s *DynamoDBStore) Status() (success bool, err error) {
	success = false
	tables, err := s.server.ListTables()
	success = err == nil && s.containsTable(tables, s.tablename)
	return true, nil
}

// Exists returns a Boolean indicating whether a device has previously
// registered with the Simple Push server. Implements Store.Exists().
func (s *DynamoDBStore) Exists(uaid string) bool {
	res, err := s.table.CountQuery([]dynamodb.AttributeComparison{
		*dynamodb.NewEqualStringAttributeComparison("uaid", uaid),
		*dynamodb.NewEqualStringAttributeComparison("chid", " "),
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
	now := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	vers := strconv.FormatInt(version, 10)
	success, err := s.table.PutItem(uaid, " ", []dynamodb.Attribute{
		*dynamodb.NewNumericAttribute("created", now),
	})
	if err != nil {
		return
	}
	// now add the chid.
	success, err = s.table.PutItem(uaid, chid, []dynamodb.Attribute{
		*dynamodb.NewNumericAttribute("version", vers),
		*dynamodb.NewNumericAttribute("created", now),
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
	success, err := s.table.UpdateAttributes(&dynamodb.Key{uaid, chid},
		[]dynamodb.Attribute{
			*dynamodb.NewNumericAttribute("version",
				strconv.FormatInt(version, 10)),
		})
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
// memcached. Deregistration calls should call s.Unregister() instead.
// Implements Store.Drop().
func (s *DynamoDBStore) Drop(uaid, chid string) (err error) {
	return s.Unregister(uaid, chid)
}

// FetchAll returns all channel updates and expired channels for a device ID
// since the specified cutoff time. Implements Store.FetchAll().
func (s *DynamoDBStore) FetchAll(uaid string, since time.Time) (updates []Update, expired []string, err error) {

	attrs := []dynamodb.AttributeComparison{
		*dynamodb.NewEqualStringAttributeComparison("uaid", uaid),
		*dynamodb.NewStringAttributeComparison("chid", dynamodb.COMPARISON_GREATER_THAN, " "),
	}
	results, err := s.table.Query(
		attrs,
	)
	if err != nil {
		return
	}
	now := time.Now().UTC().Unix()
	for _, r := range results {
		var vers string
		if vera, ok := r["version"]; ok {
			vers = vera.Value
		} else {
			if vera, ok := r["created"]; ok {
				vers = vera.Value
			} else {
				vers = strconv.FormatInt(now, 10)
			}
		}
		version, err := strconv.ParseUint(vers, 10, 64)
		if err != nil {
			version = uint64(now)
		}
		updates = append(updates, Update{
			ChannelID: r["chid"].Value,
			Version:   version,
		})
	}

	return
}

// DropAll removes all channel records for the given device ID. Implements
// Store.DropAll().
func (s *DynamoDBStore) DropAll(uaid string) (err error) {
	return
}

// FetchPing retrieves proprietary ping information for the given device ID
// from memcached. Implements Store.FetchPing().
func (s *DynamoDBStore) FetchPing(uaid string) (pingData []byte, err error) {
	result, err := s.table.Query([]dynamodb.AttributeComparison{
		*dynamodb.NewEqualStringAttributeComparison("uaid", uaid),
		*dynamodb.NewEqualStringAttributeComparison("chid", " "),
	})
	if err != nil {
		return
	}
	if len(result) > 0 {
		if pp, ok := result[0]["proprietary_ping"]; ok {
			pingData = []byte(pp.Value)
		}
	}
	return
}

// PutPing stores the proprietary ping info blob for the given device ID in
// memcached. Implements Store.PutPing().
func (s *DynamoDBStore) PutPing(uaid string, pingData []byte) (err error) {
	// s.table.
	_, err = s.table.UpdateAttributes(&dynamodb.Key{uaid, " "},
		[]dynamodb.Attribute{
			*dynamodb.NewStringAttribute("proprietary_ping", string(pingData)),
		},
	)
	return
}

// DropPing removes all proprietary ping info for the given device ID.
// Implements Store.DropPing().
func (s *DynamoDBStore) DropPing(uaid string) (err error) {
	_, err = s.table.DeleteAttributes(&dynamodb.Key{uaid, " "},
		[]dynamodb.Attribute{
			*dynamodb.NewStringAttribute("proprietary_ping", ""),
		},
	)
	return
}

func init() {
	AvailableStores["dynamodb"] = func() HasConfigStruct {
		return NewDynamoDB()
	}
}
