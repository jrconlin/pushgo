/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
	"testing"
	"time"

	"github.com/mozilla-services/pushgo/dynamodb"
)

type testDynamoServer struct {
	Target  []string
	Request [][]byte
	Reply   [][]byte
	Err     error
	CallNum int64
}

var tableJson = `{"Table":{"AttributeDefinitions":[{"AttributeName":"chid","AttributeType":"S"},{"AttributeName":"modified","AttributeType":"N"},{"AttributeName":"uaid","AttributeType":"S"}],"CreationDateTime":1.426024966598E9,"GlobalSecondaryIndexes":[{"IndexName":"uaid-modified-index","IndexSizeBytes":286,"IndexStatus":"ACTIVE","ItemCount":4,"KeySchema":[{"AttributeName":"uaid","KeyType":"HASH"},{"AttributeName":"modified","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"},"ProvisionedThroughput":{"NumberOfDecreasesToday":0,"ReadCapacityUnits":1,"WriteCapacityUnits":1}}],"ItemCount":4,"KeySchema":[{"AttributeName":"uaid","KeyType":"HASH"},{"AttributeName":"chid","KeyType":"RANGE"}],"ProvisionedThroughput":{"NumberOfDecreasesToday":0,"ReadCapacityUnits":1,"WriteCapacityUnits":1},"TableName":"test","TableSizeBytes":286,"TableStatus":"ACTIVE"}}`

func FakeNow(int64) int64 {
	return 1234567890
}

func init() {
	Now = FakeNow
}

func NewTestTable(s *testDynamoServer) (db *DynamoDBStore) {
	db = NewDynamoDB()
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
					ReadCapacityUnits:  1,
					WriteCapacityUnits: 1,
				},
			},
		},
		ProvisionedThroughput: dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  1,
			WriteCapacityUnits: 1,
		},
		Server: s,
	}
	return
}

func (r *testDynamoServer) Query(target string, query []byte) (resp []byte, err error) {
	//fmt.Printf("\n>> Query %s\n%s\n", target, string(query))
	r.Target = append(r.Target, target)
	r.Request = append(r.Request, query)
	if r.Err != nil {
		return nil, r.Err
	}
	if r.CallNum >= int64(len(r.Reply)) {
		return nil, errors.New("Tester: Too few predicted calls!")
	}
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
	testServer := &testDynamoServer{
		Reply: [][]byte{[]byte(resp)},
	}
	test := NewTestTable(testServer)

	ok, err := test.Status()
	if !ok {
		t.Errorf("Status failed")
	}
	if err != nil {
		t.Errorf("Status returned error %s", err)
	}
}

func Test_Dynamo_Exists(t *testing.T) {
	resp := `{"Count":1}`
	testServer := &testDynamoServer{
		Reply: [][]byte{[]byte(resp)},
	}
	test := NewTestTable(testServer)
	if !test.Exists("test") {
		t.Errorf("Exists failed to find record")
	}
	if string(testServer.Request[0]) != `{"KeyConditions":{"chid":{"AttributeValueList":[{"S":" "}],"ComparisonOperator":"EQ"},"uaid":{"AttributeValueList":[{"S":"test"}],"ComparisonOperator":"EQ"}},"TableName":"test"}` {
		t.Errorf("Exists: Request may not be correct.")
	}
}

func Test_Dynamo_Register(t *testing.T) {
	resp := `{}`
	testServer := &testDynamoServer{
		Reply: [][]byte{[]byte(resp), []byte(resp)},
	}
	test := NewTestTable(testServer)
	err := test.Register("test", "test", 123)
	if err != nil {
		t.Errorf("Failed to register %s", err)
	}
	if testServer.Target[0] != "UpdateItem" &&
		string(testServer.Request[0]) != `{"Key":{"chid":{"S":" "},"uaid":{"S":"test"}},"TableName":"test","ConditionExpression":"attribute_not_exists(#c)","ExpressionAttributeNames":{"#c":"created","#m":"modified"},"ExpressionAttributeValues":{":c":{"N":"1234567890"},":m":{"N":"1234567890"}},"UpdateExpression":"SET #c=:c, #m=:m","ReturnValues":"NONE"}` &&
		testServer.Target[1] != "PutItem" &&
		string(testServer.Request[1]) != `{"Item":{"chid":{"S":"test"},"created":{"N":"1234567890"},"modified":{"N":"1234567890"},"uaid":{"S":"test"},"version":{"N":"123"}},"TableName":"test"}` {
		t.Errorf("Register may not have created master and child")
	}

}

func Test_Dynamo_Update(t *testing.T) {
	resp := `{}`
	testServer := &testDynamoServer{
		Reply: [][]byte{[]byte(resp)},
	}
	test := NewTestTable(testServer)
	err := test.Update("test", "test", 123)
	if err != nil {
		t.Errorf("Failed to update %s", err)
	}
	if testServer.Target[0] != "UpdateItem" &&
		string(testServer.Request[0]) != `{"Key":{"chid":{"S":"test"},"uaid":{"S":"test"}},"TableName":"test","ConditionExpression":"#ver \u003c :ver","ExpressionAttributeNames":{"#mod":"modified","#ver":"version"},"ExpressionAttributeValues":{":mod":{"N":"1234567890"},":ver":{"N":"123"}},"UpdateExpression":"SET #ver=:ver, #mod=:mod","ReturnValues":"NONE"}` {
		t.Errorf("Failed to use UpdateItem")
	}
}

func Test_Dynamo_Unregister(t *testing.T) {
	resp := `{}`
	testServer := &testDynamoServer{
		Reply: [][]byte{[]byte(resp)},
	}
	test := NewTestTable(testServer)
	err := test.Unregister("test", "test")
	if err != nil {
		t.Errorf("Failed to unregister %s", err)
	}
	if testServer.Target[0] != "DeleteItem" {
		t.Errorf("Failed to use DeleteItem")
	}
	if string(testServer.Request[0]) != `{"Key":{"chid":{"S":"test"},"uaid":{"S":"test"}},"TableName":"test"}` {
		t.Errorf("Request may not be correct")
	}
}

func Test_Dynamo_Drop(t *testing.T) {
	resp := `{}`
	testServer := &testDynamoServer{
		Reply: [][]byte{[]byte(resp)},
	}
	test := NewTestTable(testServer)
	err := test.Drop("test", "test")
	if err != nil {
		t.Errorf("Failed to unregister %s", err)
	}
	if testServer.Target[0] != "DeleteItem" &&
		string(testServer.Request[0]) != `{"Key":{"chid":{"S":"test"},"uaid":{"S":"test"}},"TableName":"test"}` {
		t.Errorf("Failed to use DeleteItem")
	}
}

func Test_Dynamo_DropAll(t *testing.T) {
	resp := `{}`
	testServer := &testDynamoServer{
		Reply: [][]byte{[]byte(resp), []byte(resp)},
	}
	test := NewTestTable(testServer)
	err := test.DropAll("test")
	if err != nil {
		t.Errorf("Failed to unregister %s", err)
	}
	if testServer.Target[0] != "Query" &&
		string(testServer.Request[0]) != `{"KeyConditions":{"chid":{"AttributeValueList":[{"S":" "}],"ComparisonOperator":"GT"},"uaid":{"AttributeValueList":[{"S":"test"}],"ComparisonOperator":"EQ"}},"TableName":"test"}` {
		t.Errorf("Failed to use Query")
		t.Errorf("Query may not be correct")
	}
	if testServer.Target[1] != "BatchWriteItem" &&
		string(testServer.Request[1]) != `{"RequestItems":{"test":[{"DeleteRequest":{"Key":{"chid":{"S":" "},"uaid":{"S":"test"}}}}]}}` {
		t.Errorf("Request may not be correct")
	}
}

func Test_Dynamo_FetchAll(t *testing.T) {
	resp := `{}`
	testServer := &testDynamoServer{
		Reply: [][]byte{[]byte(resp)},
	}
	test := NewTestTable(testServer)
	data, expired, err := test.FetchAll("test", time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Errorf("Failed to unregister %s", err)
	}
	if testServer.Target[0] != "Query" &&
		string(testServer.Request[0]) != `{"KeyConditions":{"chid":{"AttributeValueList":[{"S":" "}],"ComparisonOperator":"GT"},"uaid":{"AttributeValueList":[{"S":"test"}],"ComparisonOperator":"EQ"}},"TableName":"test"}` {
		t.Errorf("Request may not be correct")
	}
	_ = data
	_ = expired
}

func Test_Dynamo_Ping(t *testing.T) {
	resp := `{}`
	testServer := &testDynamoServer{
		Reply: [][]byte{[]byte(resp), []byte(resp), []byte(resp)},
	}
	test := NewTestTable(testServer)
	pingData := []byte(`{pingData: test123}`)
	err := test.PutPing("test", pingData)
	if err != nil {
		t.Errorf("Failed to Put ping %s", err)
	}

	data, err := test.FetchPing("test")
	if err != nil {
		t.Errorf("Failed to fetch ping %s", err)
	}

	err = test.DropPing("test")
	if err != nil {
		t.Errorf("Failed to drop ping %s", err)
	}
	if testServer.Target[0] != "UpdateItem" &&
		string(testServer.Request[0]) != `{"Key":{"chid":{"S":" "},"uaid":{"S":"test"}},"TableName":"test","ExpressionAttributeNames":{"#modf":"modified","#ping":"proprietary_ping"},"ExpressionAttributeValues":{":modf":{"N":"1234567890"},":ping":{"B":"e3BpbmdEYXRhOiB0ZXN0MTIzfQ=="}},"UpdateExpression":"SET #ping=:ping, #modf=:modf","ReturnValues":"NONE"}` {
		t.Errorf("Request may not be correct")
	}
	if testServer.Target[1] != "Query" &&
		string(testServer.Request[1]) != `{"KeyConditions":{"chid":{"AttributeValueList":[{"S":" "}],"ComparisonOperator":"EQ"},"uaid":{"AttributeValueList":[{"S":"test"}],"ComparisonOperator":"EQ"}},"TableName":"test"}` {
		t.Errorf("Request may not be correct")
	}
	if testServer.Target[2] != "UpdateItem" &&
		string(testServer.Request[2]) != `{"Key":{"chid":{"S":" "},"uaid":{"S":"test"}},"TableName":"test","UpdateExpression":"remove proprietary_ping = :ping","ReturnValues":"NONE"}` {
		t.Errorf("Request may not be correct")
	}
	_ = data
}
