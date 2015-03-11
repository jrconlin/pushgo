/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
	"fmt"
	//	"strings"
	"testing"
	//	"time"

	//	"github.com/mozilla-services/pushgo/dynamodb"
)

type testDynamoServer struct {
	Target  string
	Request []byte
	Reply   [][]byte
	Err     error
	CallNum int64
}

var tableJson = `{"Table":{"AttributeDefinitions":[{"AttributeName":"chid","AttributeType":"S"},{"AttributeName":"modified","AttributeType":"N"},{"AttributeName":"uaid","AttributeType":"S"}],"CreationDateTime":1.426024966598E9,"GlobalSecondaryIndexes":[{"IndexName":"uaid-modified-index","IndexSizeBytes":286,"IndexStatus":"ACTIVE","ItemCount":4,"KeySchema":[{"AttributeName":"uaid","KeyType":"HASH"},{"AttributeName":"modified","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"},"ProvisionedThroughput":{"NumberOfDecreasesToday":0,"ReadCapacityUnits":1,"WriteCapacityUnits":1}}],"ItemCount":4,"KeySchema":[{"AttributeName":"uaid","KeyType":"HASH"},{"AttributeName":"chid","KeyType":"RANGE"}],"ProvisionedThroughput":{"NumberOfDecreasesToday":0,"ReadCapacityUnits":1,"WriteCapacityUnits":1},"TableName":"test","TableSizeBytes":286,"TableStatus":"ACTIVE"}}`

func NewTestTable(s *testDynamoServer) (db *DynamoDBStore) {
	db := NewDynamoDB()
	db.server = s
	db.tableName = "test"
	db.table = &dynamodb.Table{
		TableName: db.tableName,
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
		Server: s,
	}
}

func (r *testDynamoServer) Query(target string, query []byte) (resp []byte, err error) {
	fmt.Printf("\n>>== Query %s\n%s\n", target, string(query))
	r.Target = target
	r.Request = query
	if r.Err != nil {
		return nil, r.Err
	}
	if r.CallNum >= int64(len(r.Reply)) {
		return nil, errors.New("crapsticks")
	}
	fmt.Printf("Query: %d == %d \n", r.CallNum, len(r.Reply))
	resp = r.Reply[r.CallNum]
	r.CallNum++
	return
}

func Test_NewDynamoDB(t *testing.T) {
	s := NewDynamoDB()
	if s.statusTimeout == 0 {
		t.Error("statusTimeout undefined")
	}
	if s.statusIdle == 0 {
		t.Error("statusIdle undefined")
	}
}

func Test_Dynamo_ConfigStruct(t *testing.T) {
	if _, ok := NewDynamoDB().ConfigStruct().(*DynamoDBConf); !ok {
		t.Error("DynamoDB invalid configuration struct")
	}
}

func Test_Dynamo_createTable(t *testing.T) {
	testdb := NewDynamoDB()
	testdb.tableName = "test"

	// table creation usually requires at least two reads
	testServer := &testDynamoServer{
		Reply: [][]byte{[]byte(tableJson), []byte(tableJson)},
	}

	err := testdb.createTable(testServer)
	if err != nil {
		t.Errorf("DynamoDB Init reported error: %s", err.Error())
	}

}

func Test_Dynamo_CanStore(t *testing.T) {
	testDb := NewDynamoDB()
	testDb.maxChannels = 10
	if testDb.CanStore(100) {
		t.Errorf("DynamoDB CanStore allowed too many")
	}
}

func Test_Dynamo_KeyToIDs(t *testing.T) {
	testDb := NewDynamoDB()
	uaid, chid, err := testDb.KeyToIDs("test.foo")
	if uaid != "test" && chid != "foo" {
		t.Errorf("DynamoDB KeyToIDs failed to parse uaid")
	}
	if err != nil {
		t.Errorf("DynamoDB KeyToIDs returned error %s", err)
	}
}

func Test_Dynamo_IDsToKey(t *testing.T) {
	testDb := NewDynamoDB()
	uaid, err := testDb.IDsToKey("test", "foo")
	if uaid != "test.foo" {
		t.Errorf("DynamoDB IDsToKey failed to compile uaid")
	}
	if err != nil {
		t.Errorf("DynamoDB IDsToKey returned error %s", err)
	}
}

func Test_Dynamo_Status(t *testing.T) {
	resp := `{"LastEvaluatedTableName":"", "TableNames":["test"]}`
	testDb := NewDynamoDB()
	testDb.tableName = "test"
	test
	testDb.server = &testDynamoServer{
		Reply: [][]byte{[]byte(resp)},
	}

	ok, err := testDb.Status()
	if !ok {
		t.Errorf("Status failed")
	}
	if err != nil {
		t.Errorf("Status returned error %s", err)
	}
}

func Test_Dynamo_Exists(t *testing.T) {
	testDb := NewDynamoDB()
	if !testDb.Exists("test") {
		t.Errorf("Exists failed to find record")
	}
}

func Test_Dynamo_Register(t *testing.T) {
	testDb := NewDynamoDB()
	err := testDb.Register("test", "test", 123)
	if err != nil {
		t.Errorf("Failed to register %s", err)
	}
}

func Test_Dynamo_Update(t *testing.T) {
	testDb := NewDynamoDB()
	err := testDb.Update("test", "test", 123)
	if err != nil {
		t.Errorf("Failed to update %s", err)
	}
}

func Test_Dynamo_Unregister(t *testing.T) {
	testDb := NewDynamoDB()
	err := testDb.Unregister("test", "test")
	if err != nil {
		t.Errorf("Failed to unregister %s", err)
	}
}

func Test_Dynamo_Drop(t *testing.T) {
	testDb := NewDynamoDB()
	err := testDb.Unregister("test", "test")
	if err != nil {
		t.Errorf("Failed to unregister %s", err)
	}
}

func Test_Dynamo_DropAll(t *testing.T) {
	testDb := NewDynamoDB()
	err := testDb.Unregister("test", "test")
	if err != nil {
		t.Errorf("Failed to unregister %s", err)
	}
}

func Test_Dynamo_FetchAll(t *testing.T) {
	testDb := NewDynamoDB()
	err := testDb.Unregister("test", "test")
	if err != nil {
		t.Errorf("Failed to unregister %s", err)
	}
}

func Test_Dynamo_Ping(t *testing.T) {
	testDb := NewDynamoDB()
	err := testDb.Unregister("test", "test")
	if err != nil {
		t.Errorf("Failed to unregister %s", err)
	}
}
