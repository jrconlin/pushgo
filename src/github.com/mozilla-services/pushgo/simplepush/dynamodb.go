/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
	"strconv"
	"time"

	"github.com/goamz/goamz/dynamodb"
)

const (
	RFC_ISO_8601  = "20060102T150405Z"
	DYNAMO_PREFIX = "DynamoDB_20120810"
)

var (
	ErrDynamoDBInvalidRegion = errors.New("Invalid Region")
	ErrDynamoDBTimeout       = errors.New("DynamoDB function timed out")
	ErrDynamoDBFailure       = errors.New("DynamoDB returned non success")
)

// Generic interfaces (used by testing)

type dynamoTable interface {
	DescribeTable() (*dynamodb.TableDescriptionT, error)
	CountQuery([]dynamodb.AttributeComparison) (int64, error)
	PutItem(string, string, []dynamodb.Attribute) (bool, error)
	UpdateAttributes(*dynamodb.Key, []dynamodb.Attribute) (bool, error)
	DeleteItem(*dynamodb.Key) (bool, error)
	Query([]dynamodb.AttributeComparison) ([]map[string]*dynamodb.Attribute, error)
	DeleteAttributes(*dynamodb.Key, []dynamodb.Attribute) (bool, error)
	BatchWriteItems(itemActions map[string][][]dynamodb.Attribute) *dynamodb.BatchWriteItem
}

type dynamoBatchWriteItem interface {
	Execute() (map[string]interface{}, error)
}

type dynamoServer interface {
	NewTable(string, dynamodb.PrimaryKey) *dynamodb.Table
	CreateTable(dynamodb.TableDescriptionT) (string, error)
	ListTables() ([]string, error)
}

func ddb_containsTable(tableSet []string, table string) bool {
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

func ddb_getNow(t int64) string {
	if t == 0 {
		t = time.Now().UTC().Unix()
	}
	return strconv.FormatInt(t, 10)
}
