/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"testing"
	"time"

	"github.com/jrconlin/goamz/dynamodb"
)

type testDynamoServer struct {
	TestTable dynamoTable
	logger    *SimpleLogger
}

func (r *testDynamoServer) NewTable(name string, key dynamodb.PrimaryKey) (rep *dynamodb.Table) {
	table := &dynamodb.Table{
		Name: r.TestTable.(*testDynamoTable).Name,
	}
	return table
}

func (r *testDynamoServer) CreateTable(dynamodb.TableDescriptionT) (string, error) {
	return r.TestTable.(*testDynamoTable).Name, nil
}

func (r *testDynamoServer) ListTables() (tables []string, err error) {
	tables = append(tables, r.TestTable.(*testDynamoTable).Name)
	return tables, nil
}

type testDynamoTable struct {
	Name       string
	desc       []*dynamodb.TableDescriptionT
	count      int64
	item       string
	value      string
	attributes map[string][]dynamodb.Attribute
	reply      map[string]*dynamodb.Attribute
	err        error
	success    bool
}

func (r *testDynamoTable) DescribeTable() (*dynamodb.TableDescriptionT, error) {
	ret := r.desc[0]
	if len(r.desc) > 1 {
		r.desc = r.desc[1:]
	}
	return ret, r.err
}

func (r *testDynamoTable) CountQuery([]dynamodb.AttributeComparison) (int64, error) {
	return r.count, r.err
}

func (r *testDynamoTable) PutItem(hashkey string, rangekey string, attributes []dynamodb.Attribute) (bool, error) {
	if r.attributes == nil {
		r.attributes = make(map[string][]dynamodb.Attribute)
	}
	r.attributes[fmt.Sprintf("%s-%s", hashkey, rangekey)] = attributes
	return r.success, r.err
}

func (r *testDynamoTable) UpdateAttributes(key *dynamodb.Key, attributes []dynamodb.Attribute) (bool, error) {
	if r.attributes == nil {
		r.attributes = make(map[string][]dynamodb.Attribute)
	}
	r.attributes[fmt.Sprintf("%s-%s", key.HashKey, key.RangeKey)] = attributes
	return r.success, r.err
}

func (r *testDynamoTable) DeleteItem(key *dynamodb.Key) (bool, error) {
	return r.success, r.err
}

func (r *testDynamoTable) Query([]dynamodb.AttributeComparison) (reply []map[string]*dynamodb.Attribute, err error) {
	c := 0
	reply = make([]map[string]*dynamodb.Attribute, len(r.attributes))
	for chid, rr := range r.attributes {
		reply[c] = make(map[string]*dynamodb.Attribute)
		for _, ar := range rr {
			reply[c]["chid"] = &dynamodb.Attribute{
				Value: chid,
			}
			reply[c][ar.Name] = &dynamodb.Attribute{
				Value: ar.Value,
			}
		}
		c += 1
	}
	return reply, r.err
}

func (r *testDynamoTable) DeleteAttributes(key *dynamodb.Key, attr []dynamodb.Attribute) (bool, error) {
	return r.success, r.err
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

func Test_Dynamo_Init(t *testing.T) {
	testdb := NewDynamoDB()
	dbcfg := testdb.ConfigStruct()
	dbcfg.(*DynamoDBConf).TableName = "test"
	testTable := &testDynamoTable{
		Name: "test",
		desc: []*dynamodb.TableDescriptionT{
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "creating",
			},
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "active",
			},
		},
		err:     nil,
		success: true,
	}
	logger, _ := NewLogger(&TestLogger{DEBUG, t})
	testdb.server = &testDynamoServer{
		TestTable: testTable,
		logger:    logger,
	}

	// use dummy server
	testApp := &Application{log: logger}

	err := testdb.Init(testApp, dbcfg)
	if err != nil {
		t.Errorf("DynamoDB Init reported error: %s", err.Error())
	}
}

func Test_waitUntilStatus(t *testing.T) {
	testdb := NewDynamoDB()
	testTable := &testDynamoTable{
		desc: []*dynamodb.TableDescriptionT{
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "creating",
			},
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "active",
			},
		},
		err:     nil,
		success: true,
	}
	testdb.statusTimeout = time.Second * 1
	testdb.statusIdle = time.Millisecond * 10
	err := testdb.waitUntilStatus(testTable, "active")
	if err != nil {
		t.Error("waitUntilStatus returned error: %s", err.Error())
	}
}

func Test_Dynamo_Exists(t *testing.T) {
	testdb := NewDynamoDB()
	testTable := &testDynamoTable{
		desc: []*dynamodb.TableDescriptionT{
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "active",
			},
		},
		success: true,
		err:     nil,
		count:   1,
	}
	testdb.table = testTable
	testdb.statusTimeout = time.Second * 1
	testdb.statusIdle = time.Millisecond * 10
	exist := testdb.Exists("testuaid")
	if !exist {
		t.Error("Failed to exist")
	}
	testTable.count = 0
	exist = testdb.Exists("testuaid")
	if exist {
		t.Error("Failed to not exist")
	}
}

func Test_Dynamo_Register(t *testing.T) {
	var ok bool
	testdb := NewDynamoDB()
	testTable := &testDynamoTable{
		desc: []*dynamodb.TableDescriptionT{
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "active",
			},
		},
		success: true,
		err:     nil,
		count:   1,
	}
	testdb.table = testTable
	testdb.statusTimeout = time.Second * 1
	testdb.statusIdle = time.Millisecond * 10
	err := testdb.Register("testuaid", "testchid", 123)
	if err != nil {
		t.Error("Register errored %s", err.Error())
	}

	if _, ok = testTable.attributes["testuaid- "]; !ok {
		t.Error("Failed to create root record")
	}
	rr, ok := testTable.attributes["testuaid-testchid"]
	if !ok {
		t.Error("Failed to create chid record")
	}
	ok = false
	for _, r := range rr {
		if r.Name == "version" && r.Value == "123" {
			ok = true
		}
	}
	if !ok {
		t.Error("Missing or incorrect 'value' record")
	}
}

func Test_Dynamo_Update(t *testing.T) {
	var ok bool
	testdb := NewDynamoDB()
	testTable := &testDynamoTable{
		desc: []*dynamodb.TableDescriptionT{
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "active",
			},
		},
		success: true,
		err:     nil,
		count:   1,
	}
	testdb.table = testTable
	testdb.statusTimeout = time.Second * 1
	testdb.statusIdle = time.Millisecond * 10

	err := testdb.Register("testuaid", "testchid", 123)
	if err != nil {
		t.Error("Update errored %s", err.Error())
	}

	rr, ok := testTable.attributes["testuaid-testchid"]
	if !ok {
		t.Error("failed to update chid record")
	}
	ok = false
	for _, r := range rr {
		if r.Name == "version" && r.Value == "123" {
			ok = true
		}
	}
	if !ok {
		t.Error("Missing or incorrect 'value' record")
	}

}

func Test_Dynamo_Unregister(t *testing.T) {
	testdb := NewDynamoDB()
	testTable := &testDynamoTable{
		desc: []*dynamodb.TableDescriptionT{
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "active",
			},
		},
		success: true,
		err:     nil,
		count:   1,
	}
	testdb.table = testTable
	testdb.statusTimeout = time.Second * 1
	testdb.statusIdle = time.Millisecond * 10

	err := testdb.Unregister("testuaid", "testchid")
	if err != nil {
		t.Error("Failed to unregister")
	}
	testTable.success = false
	testdb.table = testTable
	err = testdb.Unregister("testuaid", "testchid")
	if err == nil && err != ErrDynamoDBFailure {
		t.Error("Failed to fail %s", err)
	}
}

func Test_Dynamo_Drop(t *testing.T) {
	testdb := NewDynamoDB()
	testTable := &testDynamoTable{
		desc: []*dynamodb.TableDescriptionT{
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "active",
			},
		},
		success: true,
		err:     nil,
		count:   1,
	}
	testdb.table = testTable
	testdb.statusTimeout = time.Second * 1
	testdb.statusIdle = time.Millisecond * 10

	err := testdb.Drop("testuaid", "testchid")
	if err != nil {
		t.Error("Failed to Drop")
	}
	testTable.success = false
	testdb.table = testTable
	err = testdb.Unregister("testuaid", "testchid")
	if err == nil && err != ErrDynamoDBFailure {
		t.Error("Failed to fail %s", err)
	}
}

func Test_Dynamo_FetchAll(t *testing.T) {
	testdb := NewDynamoDB()
	testTable := &testDynamoTable{
		desc: []*dynamodb.TableDescriptionT{
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "active",
			},
		},
		success: true,
		err:     nil,
		count:   1,
	}
	testTable.attributes = make(map[string][]dynamodb.Attribute)
	testTable.attributes["testuaid-testchid1"] = []dynamodb.Attribute{
		dynamodb.Attribute{Name: "version", Value: "1234"},
	}
	testTable.attributes["testuaid-testchid2"] = []dynamodb.Attribute{
		dynamodb.Attribute{Name: "version", Value: "5678"},
	}
	testdb.table = testTable
	testdb.statusTimeout = time.Second * 1
	testdb.statusIdle = time.Millisecond * 10

	updates, _, err := testdb.FetchAll("testuaid", time.Now())
	if err != nil {
		t.Error("Query errored: %s", err)
	}
	if len(updates) != 2 {
		t.Error("Query returned wrong number of results")
	}
}

func Test_Dynamo_Ping(t *testing.T) {
	// var ok bool
	var testPing = []byte("This is a test ping")
	testdb := NewDynamoDB()
	testTable := &testDynamoTable{
		desc: []*dynamodb.TableDescriptionT{
			&dynamodb.TableDescriptionT{
				TableName:   "test",
				TableStatus: "active",
			},
		},
		success: true,
		err:     nil,
		count:   1,
	}
	testdb.table = testTable
	testdb.statusTimeout = time.Second * 1
	testdb.statusIdle = time.Millisecond * 10

	err := testdb.PutPing("testuaid", testPing)
	if err != nil {
		t.Error("PutPing errored %s", err.Error())
	}

	p, ok := testTable.attributes["testuaid- "]
	if !ok {
		t.Error("PutPing failed to create root record")
	}
	if p[0].Name != "proprietary_ping" || p[0].Value != string(testPing) {
		t.Error("PutPing failed to store ping record")
	}

	data, err := testdb.FetchPing("testuaid")
	if err != nil {
		t.Error("FetchPing errored %s", err.Error())
	}
	if string(data) != string(testPing) {
		t.Error("FetchPing returned wrong data")
	}

	err = testdb.DropPing("testuaid")
	if err != nil {
		t.Error("DropPing errored %s", err.Error())
	}
}
