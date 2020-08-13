package amqp_datasource

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"regexp"
	"strings"

	"github.com/buger/jsonparser"
	"github.com/jensneuse/graphql-go-tools/pkg/ast"
	"github.com/jensneuse/graphql-go-tools/pkg/engine/datasource/httpclient"
	"github.com/jensneuse/graphql-go-tools/pkg/engine/plan"
	"github.com/jensneuse/graphql-go-tools/pkg/engine/resolve"
	"github.com/jensneuse/graphql-go-tools/pkg/lexer/literal"
	"github.com/streadway/amqp"
)

type Planner struct {
	conn *amqp.Connection
	v    *plan.Visitor
}

func NewPlanner(conn *amqp.Connection) *Planner {
	return &Planner{
		conn: conn,
	}
}

func (p *Planner) Register(visitor *plan.Visitor) {
	p.v = visitor
	visitor.RegisterEnterFieldVisitor(p)
}

func (p *Planner) EnterField(ref int) {
	rootField, config := p.v.IsRootField(ref)
	if !rootField {
		return
	}

	// queue
	path := config.Attributes.ValueForKey(httpclient.PATH)
	// exchange
	baseURL := config.Attributes.ValueForKey(httpclient.BASEURL)
	method := config.Attributes.ValueForKey(httpclient.METHOD)
	body := config.Attributes.ValueForKey(httpclient.BODY)
	headers := config.Attributes.ValueForKey(httpclient.HEADERS)
	queryParams := config.Attributes.ValueForKey(httpclient.QUERYPARAMS)

	queryParams = p.prepareQueryParams(ref, queryParams)

	var (
		input []byte
	)

	url := []byte(string(baseURL) + string(path))

	input = httpclient.SetInputURL(input, url)
	input = httpclient.SetInputMethod(input, method)
	input = httpclient.SetInputBody(input, body)
	input = httpclient.SetInputHeaders(input, headers)
	input = httpclient.SetInputQueryParams(input, queryParams)

	bufferID := p.v.NextBufferID()
	p.v.SetBufferIDForCurrentFieldSet(bufferID)
	p.v.SetCurrentObjectFetch(&resolve.SingleFetch{
		BufferId: bufferID,
		Input:    string(input),
		DataSource: &Source{
			conn: p.conn,
		},
		DisallowSingleFlight: !bytes.Equal(method, []byte("GET")),
	}, config)
}

type QueryValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func NewQueryValues(values ...QueryValue) []byte {
	out, _ := json.Marshal(values)
	return out
}

var (
	selectorRegex = regexp.MustCompile(`"{{\s(.*?)\s}}"`)
)

// prepareQueryParams ensures that values
func (p *Planner) prepareQueryParams(field int, params []byte) []byte {
	var (
		values        [][]byte
		deleteIndices []int
	)
	_, err := jsonparser.ArrayEach(params, func(value []byte, dataType jsonparser.ValueType, offset int, err error) {
		values = append(values, value)
	})
	if err != nil {
		return params
	}

	for i := range values {
		values[i] = selectorRegex.ReplaceAllFunc(values[i], func(b []byte) []byte {
			subs := selectorRegex.FindSubmatch(b)
			if len(subs) != 2 {
				return b
			}
			path := string(bytes.TrimPrefix(subs[1], []byte(".")))
			segments := strings.Split(path, ".")
			if len(segments) < 2 || segments[0] != "arguments" {
				return b
			}
			argName := []byte(segments[1])
			argRef, exists := p.v.Operation.FieldArgument(field, argName)
			if !exists { // field argument is not defined, we have to remove the variable
				deleteIndices = append(deleteIndices, i)
				return b
			}
			value := p.v.Operation.ArgumentValue(argRef)
			switch value.Kind {
			case ast.ValueKindVariable:
				variableName := p.v.Operation.VariableValueNameBytes(value.Ref)
				if variableDefinition, ok := p.v.Operation.VariableDefinitionByNameAndOperation(p.v.Ancestors[0].Ref, variableName); ok {
					typeRef := p.v.Operation.VariableDefinitions[variableDefinition].Type
					if p.v.Operation.TypeIsScalar(typeRef, p.v.Definition) {
						return b
					}
					return b[1 : len(b)-1]
				}
			}

			return b
		})
	}

	for i := len(deleteIndices) - 1; i >= 0; i-- {
		del := deleteIndices[i]
		values = append(values[:del], values[del+1:]...) // remove variables marked for deletion
	}

	joined := bytes.Join(values, literal.COMMA)
	return append([]byte("["), append(joined, []byte("]")...)...)
}

type Source struct {
	conn *amqp.Connection
}

var (
	uniqueIdentifier = []byte("amqp")
)

func (_ *Source) UniqueIdentifier() []byte {
	return uniqueIdentifier
}

func (s *Source) Load(ctx context.Context, input []byte, bufPair *resolve.BufPair) (err error) {
	ch, err := s.conn.Channel()
	if err != nil {
		return err
	}
	defer func() {
		_ = ch.Close()
	}()

	// create an empty temporary callback queue
	cb, err := ch.QueueDeclare(
		"",    // random name
		false, // durable
		false, // delete when unused
		true,  // exclusive
		false, // noWait
		nil,   // arguments
	)
	if err != nil {
		/*
		 * When the error return value is not nil, you can assume the queue could not be
		 *	declared with these parameters, and the channel will be closed.
		 */
		return err
	}

	correlationId := randomString(32)

	_ = ch.Publish(
		// TODO: needs to come from input, at the moment, is default exchange
		// this enables us to select a specific queue to publish to using the routing key
		"",
		"rpc_queue", // TODO: needs to come from input
		false,
		false,
		amqp.Publishing{
			ContentType:   "application/json",
			CorrelationId: correlationId,
			ReplyTo:       cb.Name,
			Body:          input,
		},
	)

	// consume the temporary callback that we created
	msgs, _ := ch.Consume(
		cb.Name,
		"",
		true,
		true,
		false,
		false,
		nil,
	)

	for d := range msgs {
		if correlationId == d.CorrelationId {
			_, err = io.Copy(bufPair.Data, bytes.NewReader(d.Body))
			if err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func randInt(min int, max int) int {
	return min + rand.Intn(max-min)
}

func randomString(l int) string {
	b := make([]byte, l)
	for i := 0; i < l; i++ {
		b[i] = byte(randInt(65, 90))
	}
	return string(b)
}
