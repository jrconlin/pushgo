/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

//TODO: Add tests

package dynamodb

import (
	"encode/json"
	"errors"
	"strconv"
	"time"

	"github.com/goamz/goamz/aws"
)

const (
	DYNAMO_PREFIX = "DynamoDB_20120810"
)

var (
	ErrDynamoDBInvalidRegion = errors.New("Invalid Region")
	ErrDynamoDBTimeout       = errors.New("DynamoDB function timed out")
	ErrDynamoDBFailure       = errors.New("DynamoDB returned non success")
	ErrDynamoDBNoServer      = errors.New("No Server defined for table object")
)

type Server struct {
	Auth   aws.Auth
	Region aws.Region
}

type Error struct {
	StatusCode int
	Status     string
	Code       string
	Message    string
}

type AWSError struct {
	Type    string `json:"__type"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

func buildError(r *http.Response, jsonBody []byte) error {
	log.Printf("!!!!!! Got Error! %s\n", jsonBody)
	err := Error{
		StatusCode: r.StatusCode,
		Status:     r.Status,
	}

	awsErr := &AWSError{}
	err := json.Unmarshal(jsonBody, &awsErr)
	if err != nil {
		return err
	}
	err.Code = awsErr.Type
	parts := strings.Split(awsErr.Type, "#")
	if len(parts) == 2 {
		err.Code = parts[1]
	}
	err.Message = awsErr.Message
	return err
}

func (s *Server) Query(target string, query []byte) ([]byte, error) {
	data := strings.NewReader(string(query))
	req, err := http.NewRequest("POST", s.Region.DynamoDBEndpoint+"/", data)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Date", time.Now().UTC().FOrmat(aws.ISO8601BasicFormat))
	req.Header.Set("X-Amz-Target", DYNAMO_PREFIX+"."+target)

	token := s.Auth.Token()
	if token != "" {
		req.Header.Set("X-Amz-Security-Token", token)
	}

	signer := aws.NewV4Signer(s.Auth, "dynamodb", s.Region)
	signer.Sign(req)
	resp, err := http.DefaultClient.Do(hreq)

	if err != nil {
		log.Printf("AWS Call Failure, %s", err.Error())
		return nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Could not read response body %s", err.Error())
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, buildError(resp, body)
	}
	return body, nil
}

const (
	DDB_BLOB          = "B"
	DDB_BOOLEAN       = "BOOL"
	DDB_BLOBSET       = "BS"
	DDB_ATTRIBUTELIST = "L"
	DDB_ATTRIBUTEMAP  = "M"
	DDB_NUMBER        = "N"
	DDB_NUMBERSET     = "NS"
	DDB_NULL          = "NULL"
	DDB_STRING        = "S"
	DDB_STRINGSET     = "SS"
)

// DDB_TYPE : data
type Attribute map[string]interface{}

func (r *Attribute) Type() string {
	return r.Keys()[0]
}

// It's possible to create functions that use reflect to autoconvert
// interface{} into the appropriate DBB_* type and back again.
// But it's expensive as hell.
// Don't be lazy.

type TableRequestItem struct {
	// Do you want strongly consistent read?
	ConsistentRead bool
	// "#" + Alias : Name (e.g. "#P" : "Percentile"
	ExpressionAttributeNames map[string]string
	// For each primary key, provide ALL key values, e.g.
	// for a key with "uaid" hash and "chid" range:
	// [{"uaid":"test", "chid":" "}]
	Keys []Attribute
	// see http://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Expressions.AccessingItemAttributes.html
	ProjectionExpression string
}

type TableReply struct {
	//Map of tableName
	RequestItems map[string]TableRequestItem
	// Set to "INDEXES | TOTAL | NONE"
	ReturnConsumedCapacity string
}

type CapacityUnits struct {
	CapacityUnits int64
}

type ConsumedCapacity struct {
	CapacityUnits          int64
	GlobalSecondaryIndexes map[string]CapacityUnits
	LocalSecondaryIndexes  map[string]CapacityUnits
	Table                  map[string]CapacityUnits
}

type RequestItem struct {
	AttributesToGet          []string
	ConsistentRead           bool
	ExpressionAttributeNames map[string]string
	Keys                     []Attribute
	ProjectionExpression     string
}

type BatchItemReply struct {
	// Optional capacity report (if Request ReturnConsumedCapcity != "NONE")
	ConsumedCapacity []ConsumedCapacity
	//"tableName":[]Attributes
	Responses map[string][]Attribute
	//"tableName":BatchGetQuery
	UnprocessedKeys map[string]RequestItem
}

type KeySchema struct {
	// Key Name
	AttributeName string
	// "HASH" | "RANGE"
	KeyType string
}

type Projection struct {
	NonKeyAttributes []string
	ProjectionType   string
}

type ProvisionedThroughput struct {
	ReadCapacityUnits  int64
	WriteCapacityUnits int64
}

type SecondaryIndex struct {
	IndexName             string
	KeySchema             []KeySchema
	Projection            Projection
	ProvisionedThroughput ProvisionedThroughput
}

type AttributeDefinition struct {
	AttributeName string
	AttributeType string
}

type IndexDel struct {
	IndexName string
}

type IndexUpd struct {
	IndexName             string
	ProvisionedThroughput ProvisionedThroughput
}

type SecondaryUpdates struct {
	Create SecondaryIndex
	Delete IndexDel
	Update IndexUpd
}

type Table struct {
	Server                 DynamoServer `json:-`
	AttributeDefinitions   []AttributeDefinition
	CreationDateTime       int64 `json:omitempty`
	GlobalSecondaryIndexes []SecondaryIndex
	// Only used for TableUpdates
	GlobalSecondaryIndexUpdates []SecondaryUpdates `json:omitempty`
	ItemCount                   int64              `json:omitempty`
	KeySchema                   []KeySchema
	LocalSecondaryIndexes       []SecondaryIndex
	ProvisionedThroughput       ProvisionedThroughput
	TableName                   string
	TableSizeBytes              int64  `json:omitempty`
	TableStatus                 string `json:omitempty`
}

type Item struct {
	ConsistentRead           bool
	ExpressionAttributeNames map[string]string
	Key                      map[string]Attribute
	ProjectionExpression     string
	ReturnConsumedCapacity   string
	TableName                string
}

type BatchItemReply struct {
	ConsumedCapacity ConsumedCapacity
	Item             map[string]Attribute
}

type BatchGetQuery struct {
	RequestItems           map[string]RequestItem
	ReturnConsumedCapacity string
}

//batchGet returns BatchItemReply

type BatchDelete struct {
	Key Attribute
}

type BatchPut struct {
	Item Attribute
}

type BatchWriteRequestItem struct {
	DeleteRequest BatchKey
	PutRequest    BatchPut
}

type BatchWriteQuery struct {
	RequestItems                map[string]BatchWriteRequestItem
	ReturnConsumedCapacity      string
	ReturnItemCollectionMetrics string
}

type ItemCollectionMetrics struct {
	ItemCollectionKey   Attribute
	SizeEstimateRangeGB int64
}

type BatchWriteReply struct {
	ConsumedCapacity      ConsumedCapacity
	ItemCollectionMetrics map[string][]ItemCollectionMetrics
	UnprocessedItems      map[string][]BatchWriteRequestItem
}

type ItemRequest struct {
	Key                         map[string]Attribute
	TableName                   string
	ExpressionAttributeNames    map[string]string
	ExpressionAttributeValues   map[string]Attribute
	ReturnConsumedCapacity      string
	ReturnItemCollectionMetrics string
	ReturnValues                string
	ConditionExpression         string
}

type ItemReply struct {
	Attributes            map[string]Attribute
	ConsumedCapacity      ConsumedCapacity
	ItemCollectionMetrics ItemCollectionMetrics
}

type KeyCondition struct {
	AttributeValueList []Attribute
	ComparisonOperator string
}

type ItemQuery struct {
	KeyConditions             map[string]KeyCondition
	ConditionalOperator       string
	ConsistenRead             bool                 `json:omitempty`
	ExclusiveStartKey         map[string]Attribute `json:omitempty`
	ExpressionAttributeNames  map[string]string    `json:omitempty`
	ExpressionAttributeValues map[string]Attribute `json:omitempty`
	FilterExpression          string               `json:omitempty`
	IndexName                 string               `json:omitempty`
	Limit                     int64                `json:omitempty`
	ProjectionExpression      string               `json:omitempty`
	ReturnConsumedCapacity    string               `json:omitempty`
	ScanIndexForward          bool                 `json:omitempty`
	Select                    string               `json:omitempty`
	TableName                 string
}

type QueryResponse struct {
	ConsumedCapacity ConsumedCapacity
	Count            int64
	Items            []Attribute
	LastEvaluatedKey Attribute
	ScannedCount     int64
}

type ItemUpdate struct {
	Key                 map[string]Attribute
	TableName           string
	ConditionExpression string
	ConditionalOperator string
}

// Generic interfaces (used by testing)

// === Tables
type DynamoTable interface {
	// Create table from description
	Create() (*Table, error)
	// return description from name
	Update() (*Table, error)
	DeleteItem(*ItemRequest) (*ItemReply, error)
	GetItem(*ItemRequest) (*ItemReply, error)
	PutItem(*ItemRequest) (*ItemReply, error)
	Query(*ItemQuery) (*QueryResponse, error)
	Scan(*ItemQuery) (*QueryResponse, error)
	UpdateItem(*ItemUpdate) (*ItemReply, error)
}

type DynamoServer interface {
	Query(string, []byte) ([]byte, error)
}

// Class method
func DescribeTable(server *Server, tableName string) (table *Table, err error) {
	req := json.Marshal(struct{ TableName string }{tableName})
	target := "DescribeTable"
	// send query,
	resp, err := serverQuery(target, req)
	if err != nil {
		return
	}
	// unmarshal to *table
	table = &Table{}
	err = json.Unmarshal(resp, table)
	return
}

func DeleteTable(server *Server, tableName string) (err error) {
	req := json.Marshal(struct{ TableName string }{tableName})
	target := "DeleteTable"
	// send query
	_, err = server.Query(target, req)
	return
}

type TableList struct {
	LastEvaluatedTableName string
	TableNames             []string
}

func ListTables(server Server, fromTable string, limit int64) (tables []string, lastEvaluatedTableName string, err error) {
	tableList := &tableList
	req, err := json.Marshal(struct {
		ExclusiveStartTableName string `json:omitempty`
		Limit                   int64
	}{fromTable, limit})
	target := "ListTables"
	// send to server
	resp, err := server.Query(target, req)
	if err != nil {
		return
	}
	// unmarshal reply to tableList
	tableList := &TableList{}
	err = json.Unmarshal(resp, tableList)
	if err != nil {
		return
	}
	return tableList.TableNames, tableList.LastEvaluatedTableName, nil
}

func (t *Table) modTable(target string) (table *Table, err error) {
	if t.Server == nil {
		return t, ErrDynamoDBNoServer
	}
	jsquery, err := json.Marshal(t)
	if err != nil {
		return t, err
	}
	// send to server
	resp, err = t.Server.Query(target, jsquery)
	if err == nil {
		table = &Table{}
		err = json.Unmarshal(resp, table)
	}
	return
}

func (t *Table) Create() (table *Table, err error) {
	//
	table, err = t.modTable("CreateTable")
	// TODO query table state until "ACTIVE"
	return
}

func (t *Table) Update() (table *Table, err error) {
	return t.modTable("UpdateTable")
}

// === Items

func (t *Table) itemAction(target string, query *ItemRequest) (reply *ItemReply, err error) {
	if query.TableName == "" {
		query.TableName = t.TableName
	}
	req, err := json.Marshal(query)
	if err != nil {
		return
	}
	resp, err := t.Server.Query(target, req)
	if err != nil {
		return
	}
	reply = &ItemReply{}
	err = json.Unmarshal(resp, reply)
	return
}

func (t *Table) DeleteItem(query *ItemRequest) (reply *ItemReply, err error) {
	return t.itemAction("DeleteItem", query)
}

func (t *Table) GetItem(query *ItemRequest) (reply *ItemReply, err error) {
	return t.itemAction("GetItem", query)
}

func (t *Table) PutItem(query *ItemRequest) (reply *ItemReply, err error) {
	return t.ItemAction("PutItem", query)
}

func (t *Table) query(target string, query *ItemQuery) (reply *QueryResponse, err error) {
	if query.TableName == "" {
		query.TableName = t.TableName
	}
	req, err := json.Marshal(query)
	if err != nil {
		return
	}
	resp, err = t.Server.Query("Query", req)
	if err != nil {
		return
	}
	reply = &QueryResponse{}
	err = json.Unmarshal(resp, reply)
	return
}

func (t *Table) Query(query *ItemQuery) (reply *QueryResponse, err error) {
	return t.query("Query", query)
}

func (t *Table) Scan(query *ItemQuery) (reply *QueryResponse, err error) {
	return t.query("Scan", query)
}

func (t *Table) UpdateItem(query *ItemUpdate) (reply *ItemReply, err error) {
	if query.TableName == "" {
		query.TableName = t.TableName
	}
	req, err := json.Marshal(query)
	if err != nil {
		return
	}
	resp, err = t.Server.Query("UpdateItem", req)
	if err != nil {
		return
	}
	reply = &ItemReply{}
	err = json.Unmarshal(resp, reply)
	return
}

// === Batch funcs
func BatchGetItem(server *Server, query *BatchGetQuery) (response []BatchItemReply, err error) {
	//TODO: verify
	req, err := json.Marshal(query)
	if err != nil {
		return err
	}
	// send to server
	resp, err := server.Query("BatchGetItem", req)
	if err != nil {
		return err
	}
	err = json.Unmarshal(resp, reply)
	return
}

func BatchWriteItem(server *Server, query *BatchWriteQuery) (reply *BatchWriteReply, err error) {
	//TODO: verify
	req, err := json.Marshal(query)
	if err != nil {
		return
	}
	// send to server
	resp, err := server.Query("BatchWriteItem", req)
	if err != nil {
		return
	}
	err = json.Unmarshal(resp, reply)
	return
}

// === Utility
func SetContains(set []string, item string) bool {
	if len(set) == 0 {
		return false
	}
	for _, t := range set {
		if t == table {
			return true
		}
	}
	return false
}

func Now(t int64) string {
	if t == 0 {
		t = time.Now().UTC().Unix()
	}
	return strconv.FormatInt(t, 10)
}
