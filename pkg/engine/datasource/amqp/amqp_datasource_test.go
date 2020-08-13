package amqp_datasource

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/streadway/amqp"

	"github.com/jensneuse/graphql-go-tools/pkg/engine/resolve"
	"github.com/stretchr/testify/assert"
)

const (
	schema = `
		type Query {
			friend: Friend
			withArgument(id: String!, name: String, optional: String): Friend
			withArrayArguments(names: [String]): Friend
		}

		type Friend {
			name: String
			pet: Pet
		}

		type Pet {
			id: String
			name: String
		}
	`

	simpleOperation = `
		query {
			friend {
				name
			}
		}
	`
	nestedOperation = `
		query {
			friend {
				name
				pet {
					id
					name
				}
			}
		}
	`

	argumentOperation = `
		query ArgumentQuery($idVariable: String!) {
			withArgument(id: $idVariable, name: "foo") {
				name
			}
		}
	`

	arrayArgumentOperation = `
		query ArgumentQuery {
			withArrayArguments(names: ["foo","bar"]) {
				name
			}
		}
	`
)

//func TestAMQPDataSourcePlanning(t *testing.T) {
//	t.Run("get request", datasourcetesting.RunTest(schema, nestedOperation, "",
//		&plan.SynchronousResponsePlan{
//			Response: resolve.GraphQLResponse{
//				Data: &resolve.Object{
//					Fetch: &resolve.SingleFetch{
//						BufferId: 0,
//						Input:    `{"method":"GET","url":"https://example.com/friend"}`,
//						InputTemplate: resolve.InputTemplate{
//							Segments: []resolve.TemplateSegment{
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`{"method":"GET","url":"https://example.com/friend"}`),
//								},
//							},
//						},
//						DataSource: &Source{
//							client: NewPlanner(nil).clientOrDefault(),
//						},
//					},
//					FieldSets: []resolve.FieldSet{
//						{
//							BufferID:  0,
//							HasBuffer: true,
//							Fields: []resolve.Field{
//								{
//									Name: []byte("friend"),
//									Value: &resolve.Object{
//										Nullable: true,
//										Fetch: &resolve.SingleFetch{
//											BufferId: 1,
//											Input:    `{"method":"GET","url":"https://example.com/friend/$$0$$/pet"}`,
//											InputTemplate: resolve.InputTemplate{
//												Segments: []resolve.TemplateSegment{
//													{
//														SegmentType: resolve.StaticSegmentType,
//														Data:        []byte(`{"method":"GET","url":"https://example.com/friend/`),
//													},
//													{
//														SegmentType:        resolve.VariableSegmentType,
//														VariableSource:     resolve.VariableSourceObject,
//														VariableSourcePath: []string{"name"},
//													},
//													{
//														SegmentType: resolve.StaticSegmentType,
//														Data:        []byte(`/pet"}`),
//													},
//												},
//											},
//											DataSource: &Source{
//												client: NewPlanner(nil).clientOrDefault(),
//											},
//											Variables: resolve.NewVariables(
//												&resolve.ObjectVariable{
//													Path: []string{"name"},
//												},
//											),
//										},
//										FieldSets: []resolve.FieldSet{
//											{
//												Fields: []resolve.Field{
//													{
//														Name: []byte("name"),
//														Value: &resolve.String{
//															Path:     []string{"name"},
//															Nullable: true,
//														},
//													},
//												},
//											},
//											{
//												HasBuffer: true,
//												BufferID:  1,
//												Fields: []resolve.Field{
//													{
//														Name: []byte("pet"),
//														Value: &resolve.Object{
//															Nullable: true,
//															FieldSets: []resolve.FieldSet{
//																{
//																	Fields: []resolve.Field{
//																		{
//																			Name: []byte("id"),
//																			Value: &resolve.String{
//																				Path:     []string{"id"},
//																				Nullable: true,
//																			},
//																		},
//																		{
//																			Name: []byte("name"),
//																			Value: &resolve.String{
//																				Path:     []string{"name"},
//																				Nullable: true,
//																			},
//																		},
//																	},
//																},
//															},
//														},
//													},
//												},
//											},
//										},
//									},
//								},
//							},
//						},
//					},
//				},
//			},
//		},
//		plan.Configuration{
//			DataSourceConfigurations: []plan.DataSourceConfiguration{
//				{
//					TypeName:   "Query",
//					FieldNames: []string{"friend"},
//					Attributes: []plan.DataSourceAttribute{
//						{
//							Key:   "path",
//							Value: []byte("https://example.com/friend"),
//						},
//						{
//							Key:   "method",
//							Value: []byte("GET"),
//						},
//					},
//					DataSourcePlanner: &Planner{},
//				},
//				{
//					TypeName:   "Friend",
//					FieldNames: []string{"pet"},
//					Attributes: []plan.DataSourceAttribute{
//						{
//							Key:   "path",
//							Value: []byte("https://example.com/friend/{{ .object.name }}/pet"),
//						},
//						{
//							Key:   "method",
//							Value: []byte("GET"),
//						},
//					},
//					DataSourcePlanner: &Planner{},
//				},
//			},
//			FieldMappings: []plan.FieldMapping{
//				{
//					TypeName:              "Query",
//					FieldName:             "friend",
//					DisableDefaultMapping: true,
//				},
//				{
//					TypeName:              "Friend",
//					FieldName:             "pet",
//					DisableDefaultMapping: true,
//				},
//			},
//		},
//	))
//	t.Run("get request with argument", datasourcetesting.RunTest(schema, argumentOperation, "ArgumentQuery",
//		&plan.SynchronousResponsePlan{
//			Response: resolve.GraphQLResponse{
//				Data: &resolve.Object{
//					Fetch: &resolve.SingleFetch{
//						BufferId: 0,
//						Input:    `{"method":"GET","url":"https://example.com/$$0$$/$$1$$"}`,
//						InputTemplate: resolve.InputTemplate{
//							Segments: []resolve.TemplateSegment{
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`{"method":"GET","url":"https://example.com/`),
//								},
//								{
//									SegmentType:        resolve.VariableSegmentType,
//									VariableSource:     resolve.VariableSourceContext,
//									VariableSourcePath: []string{"idVariable"},
//								},
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`/`),
//								},
//								{
//									SegmentType:        resolve.VariableSegmentType,
//									VariableSource:     resolve.VariableSourceContext,
//									VariableSourcePath: []string{"a"},
//								},
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`"}`),
//								},
//							},
//						},
//						DataSource: &Source{
//							client: NewPlanner(nil).clientOrDefault(),
//						},
//						Variables: resolve.NewVariables(
//							&resolve.ContextVariable{
//								Path: []string{"idVariable"},
//							},
//							&resolve.ContextVariable{
//								Path: []string{"a"},
//							},
//						),
//					},
//					FieldSets: []resolve.FieldSet{
//						{
//							BufferID:  0,
//							HasBuffer: true,
//							Fields: []resolve.Field{
//								{
//									Name: []byte("withArgument"),
//									Value: &resolve.Object{
//										Nullable: true,
//										FieldSets: []resolve.FieldSet{
//											{
//												Fields: []resolve.Field{
//													{
//														Name: []byte("name"),
//														Value: &resolve.String{
//															Path:     []string{"name"},
//															Nullable: true,
//														},
//													},
//												},
//											},
//										},
//									},
//								},
//							},
//						},
//					},
//				},
//			},
//		},
//		plan.Configuration{
//			DataSourceConfigurations: []plan.DataSourceConfiguration{
//				{
//					TypeName:   "Query",
//					FieldNames: []string{"withArgument"},
//					Attributes: []plan.DataSourceAttribute{
//						{
//							Key:   "path",
//							Value: []byte("https://example.com/{{ .arguments.id }}/{{ .arguments.name }}"),
//						},
//						{
//							Key:   "method",
//							Value: []byte("GET"),
//						},
//					},
//					DataSourcePlanner: &Planner{},
//				},
//			},
//			FieldMappings: []plan.FieldMapping{
//				{
//					TypeName:              "Query",
//					FieldName:             "withArgument",
//					DisableDefaultMapping: true,
//				},
//			},
//		},
//	))
//	t.Run("post request with body", datasourcetesting.RunTest(schema, simpleOperation, "",
//		&plan.SynchronousResponsePlan{
//			Response: resolve.GraphQLResponse{
//				Data: &resolve.Object{
//					Fetch: &resolve.SingleFetch{
//						BufferId: 0,
//						Input:    `{"body":{"foo":"bar"},"method":"POST","url":"https://example.com/friend"}`,
//						InputTemplate: resolve.InputTemplate{
//							Segments: []resolve.TemplateSegment{
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`{"body":{"foo":"bar"},"method":"POST","url":"https://example.com/friend"}`),
//								},
//							},
//						},
//						DataSource: &Source{
//							client: NewPlanner(nil).clientOrDefault(),
//						},
//						DisallowSingleFlight: true,
//					},
//					FieldSets: []resolve.FieldSet{
//						{
//							BufferID:  0,
//							HasBuffer: true,
//							Fields: []resolve.Field{
//								{
//									Name: []byte("friend"),
//									Value: &resolve.Object{
//										Nullable: true,
//										FieldSets: []resolve.FieldSet{
//											{
//												Fields: []resolve.Field{
//													{
//														Name: []byte("name"),
//														Value: &resolve.String{
//															Path:     []string{"name"},
//															Nullable: true,
//														},
//													},
//												},
//											},
//										},
//									},
//								},
//							},
//						},
//					},
//				},
//			},
//		},
//		plan.Configuration{
//			DataSourceConfigurations: []plan.DataSourceConfiguration{
//				{
//					TypeName:   "Query",
//					FieldNames: []string{"friend"},
//					Attributes: []plan.DataSourceAttribute{
//						{
//							Key:   "base_url",
//							Value: []byte("https://example.com"),
//						},
//						{
//							Key:   "path",
//							Value: []byte("/friend"),
//						},
//						{
//							Key:   "method",
//							Value: []byte("POST"),
//						},
//						{
//							Key:   "body",
//							Value: []byte(`{"foo":"bar"}`),
//						},
//					},
//					DataSourcePlanner: &Planner{},
//				},
//			},
//			FieldMappings: []plan.FieldMapping{
//				{
//					TypeName:              "Query",
//					FieldName:             "friend",
//					DisableDefaultMapping: true,
//				},
//			},
//		},
//	))
//	t.Run("get request with headers", datasourcetesting.RunTest(schema, simpleOperation, "",
//		&plan.SynchronousResponsePlan{
//			Response: resolve.GraphQLResponse{
//				Data: &resolve.Object{
//					Fetch: &resolve.SingleFetch{
//						BufferId: 0,
//						Input:    `{"headers":{"Authorization":"Bearer 123","X-API-Key":"456"},"method":"GET","url":"https://example.com/friend"}`,
//						InputTemplate: resolve.InputTemplate{
//							Segments: []resolve.TemplateSegment{
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`{"headers":{"Authorization":"Bearer 123","X-API-Key":"456"},"method":"GET","url":"https://example.com/friend"}`),
//								},
//							},
//						},
//						DataSource: &Source{
//							client: NewPlanner(nil).clientOrDefault(),
//						},
//					},
//					FieldSets: []resolve.FieldSet{
//						{
//							BufferID:  0,
//							HasBuffer: true,
//							Fields: []resolve.Field{
//								{
//									Name: []byte("friend"),
//									Value: &resolve.Object{
//										Nullable: true,
//										FieldSets: []resolve.FieldSet{
//											{
//												Fields: []resolve.Field{
//													{
//														Name: []byte("name"),
//														Value: &resolve.String{
//															Path:     []string{"name"},
//															Nullable: true,
//														},
//													},
//												},
//											},
//										},
//									},
//								},
//							},
//						},
//					},
//				},
//			},
//		},
//		plan.Configuration{
//			DataSourceConfigurations: []plan.DataSourceConfiguration{
//				{
//					TypeName:   "Query",
//					FieldNames: []string{"friend"},
//					Attributes: []plan.DataSourceAttribute{
//						{
//							Key:   "base_url",
//							Value: []byte("https://example.com"),
//						},
//						{
//							Key:   "path",
//							Value: []byte("/friend"),
//						},
//						{
//							Key:   "method",
//							Value: []byte("GET"),
//						},
//						{
//							Key:   "headers",
//							Value: []byte(`{"Authorization":"Bearer 123","X-API-Key":"456"}`),
//						},
//					},
//					DataSourcePlanner: &Planner{},
//				},
//			},
//			FieldMappings: []plan.FieldMapping{
//				{
//					TypeName:              "Query",
//					FieldName:             "friend",
//					DisableDefaultMapping: true,
//				},
//			},
//		},
//	))
//	t.Run("get request with query", datasourcetesting.RunTest(schema, argumentOperation, "ArgumentQuery",
//		&plan.SynchronousResponsePlan{
//			Response: resolve.GraphQLResponse{
//				Data: &resolve.Object{
//					Fetch: &resolve.SingleFetch{
//						BufferId: 0,
//						Input:    `{"query_params":[{"name":"static","value":"staticValue"},{"name":"static","value":"secondStaticValue"},{"name":"name","value":"$$0$$"},{"name":"id","value":"$$1$$"}],"method":"GET","url":"https://example.com/friend"}`,
//						InputTemplate: resolve.InputTemplate{
//							Segments: []resolve.TemplateSegment{
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`{"query_params":[{"name":"static","value":"staticValue"},{"name":"static","value":"secondStaticValue"},{"name":"name","value":"`),
//								},
//								{
//									SegmentType:        resolve.VariableSegmentType,
//									VariableSource:     resolve.VariableSourceContext,
//									VariableSourcePath: []string{"a"},
//								},
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`"},{"name":"id","value":"`),
//								},
//								{
//									SegmentType:        resolve.VariableSegmentType,
//									VariableSource:     resolve.VariableSourceContext,
//									VariableSourcePath: []string{"idVariable"},
//								},
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`"}],"method":"GET","url":"https://example.com/friend"}`),
//								},
//							},
//						},
//						DataSource: &Source{
//							client: NewPlanner(nil).clientOrDefault(),
//						},
//						Variables: resolve.NewVariables(
//							&resolve.ContextVariable{
//								Path: []string{"a"},
//							},
//							&resolve.ContextVariable{
//								Path: []string{"idVariable"},
//							},
//						),
//					},
//					FieldSets: []resolve.FieldSet{
//						{
//							BufferID:  0,
//							HasBuffer: true,
//							Fields: []resolve.Field{
//								{
//									Name: []byte("withArgument"),
//									Value: &resolve.Object{
//										Nullable: true,
//										FieldSets: []resolve.FieldSet{
//											{
//												Fields: []resolve.Field{
//													{
//														Name: []byte("name"),
//														Value: &resolve.String{
//															Path:     []string{"name"},
//															Nullable: true,
//														},
//													},
//												},
//											},
//										},
//									},
//								},
//							},
//						},
//					},
//				},
//			},
//		},
//		plan.Configuration{
//			DataSourceConfigurations: []plan.DataSourceConfiguration{
//				{
//					TypeName:   "Query",
//					FieldNames: []string{"withArgument"},
//					Attributes: []plan.DataSourceAttribute{
//						{
//							Key:   "base_url",
//							Value: []byte("https://example.com"),
//						},
//						{
//							Key:   "path",
//							Value: []byte("/friend"),
//						},
//						{
//							Key:   "method",
//							Value: []byte("GET"),
//						},
//						{
//							Key: "query_params",
//							Value: NewQueryValues(
//								QueryValue{
//									Name:  "static",
//									Value: "staticValue",
//								},
//								QueryValue{
//									Name:  "static",
//									Value: "secondStaticValue",
//								},
//								QueryValue{
//									Name:  "name",
//									Value: "{{ .arguments.name }}",
//								},
//								QueryValue{
//									Name:  "id",
//									Value: "{{ .arguments.id }}",
//								},
//								QueryValue{
//									Name:  "id",
//									Value: "{{ .arguments.optional }}",
//								},
//							),
//						},
//					},
//					DataSourcePlanner: &Planner{},
//				},
//			},
//			FieldMappings: []plan.FieldMapping{
//				{
//					TypeName:              "Query",
//					FieldName:             "withArgument",
//					DisableDefaultMapping: true,
//				},
//			},
//		},
//	))
//	t.Run("get request with array query", datasourcetesting.RunTest(schema, arrayArgumentOperation, "ArgumentQuery",
//		&plan.SynchronousResponsePlan{
//			Response: resolve.GraphQLResponse{
//				Data: &resolve.Object{
//					Fetch: &resolve.SingleFetch{
//						BufferId: 0,
//						Input:    `{"query_params":[{"name":"names","value":$$0$$}],"method":"GET","url":"https://example.com/friend"}`,
//						InputTemplate: resolve.InputTemplate{
//							Segments: []resolve.TemplateSegment{
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`{"query_params":[{"name":"names","value":`),
//								},
//								{
//									SegmentType:        resolve.VariableSegmentType,
//									VariableSource:     resolve.VariableSourceContext,
//									VariableSourcePath: []string{"a"},
//								},
//								{
//									SegmentType: resolve.StaticSegmentType,
//									Data:        []byte(`}],"method":"GET","url":"https://example.com/friend"}`),
//								},
//							},
//						},
//						DataSource: &Source{
//							client: NewPlanner(nil).clientOrDefault(),
//						},
//						Variables: resolve.NewVariables(
//							&resolve.ContextVariable{
//								Path: []string{"a"},
//							},
//						),
//					},
//					FieldSets: []resolve.FieldSet{
//						{
//							BufferID:  0,
//							HasBuffer: true,
//							Fields: []resolve.Field{
//								{
//									Name: []byte("withArrayArguments"),
//									Value: &resolve.Object{
//										Nullable: true,
//										FieldSets: []resolve.FieldSet{
//											{
//												Fields: []resolve.Field{
//													{
//														Name: []byte("name"),
//														Value: &resolve.String{
//															Path:     []string{"name"},
//															Nullable: true,
//														},
//													},
//												},
//											},
//										},
//									},
//								},
//							},
//						},
//					},
//				},
//			},
//		},
//		plan.Configuration{
//			DataSourceConfigurations: []plan.DataSourceConfiguration{
//				{
//					TypeName:   "Query",
//					FieldNames: []string{"withArrayArguments"},
//					Attributes: []plan.DataSourceAttribute{
//						{
//							Key:   "base_url",
//							Value: []byte("https://example.com"),
//						},
//						{
//							Key:   "path",
//							Value: []byte("/friend"),
//						},
//						{
//							Key:   "method",
//							Value: []byte("GET"),
//						},
//						{
//							Key: "query_params",
//							Value: NewQueryValues(
//								QueryValue{
//									Name:  "names",
//									Value: "{{ .arguments.names }}",
//								},
//							),
//						},
//					},
//					DataSourcePlanner: &Planner{},
//				},
//			},
//			FieldMappings: []plan.FieldMapping{
//				{
//					TypeName:              "Query",
//					FieldName:             "withArrayArguments",
//					DisableDefaultMapping: true,
//				},
//			},
//		},
//	))
//}

func TestAMQPDataSource_Load(t *testing.T) {

	runTests := func(t *testing.T, source *Source) {
		t.Run("simple get", func(t *testing.T) {

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, r.Method, http.MethodGet)
				_, _ = w.Write([]byte(`ok`))
			}))

			defer server.Close()

			input := []byte(fmt.Sprintf(`{"method":"GET","url":"%s"}`, server.URL))
			pair := resolve.NewBufPair()
			err := source.Load(context.Background(), input, pair)
			assert.NoError(t, err)
			assert.Equal(t, string(input), pair.Data.String())
		})
		t.Run("get with query parameters", func(t *testing.T) {

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, r.Method, http.MethodGet)
				fooQueryParam := r.URL.Query().Get("foo")
				assert.Equal(t, fooQueryParam, "bar")
				doubleQueryParam := r.URL.Query()["double"]
				assert.Len(t, doubleQueryParam, 2)
				assert.Equal(t, "first", doubleQueryParam[0])
				assert.Equal(t, "second", doubleQueryParam[1])
				_, _ = w.Write([]byte(`ok`))
			}))

			defer server.Close()

			input := []byte(fmt.Sprintf(`{"query_params":[{"name":"foo","value":"bar"},{"name":"double","value":"first"},{"name":"double","value":"second"}],"method":"GET","url":"%s"}`, server.URL))
			pair := resolve.NewBufPair()
			err := source.Load(context.Background(), input, pair)
			assert.NoError(t, err)
			assert.Equal(t, string(input), pair.Data.String())
		})
		t.Run("get with headers", func(t *testing.T) {

			authorization := "Bearer 123"
			xApiKey := "456"

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, r.Method, http.MethodGet)
				assert.Equal(t, authorization, r.Header.Get("Authorization"))
				assert.Equal(t, xApiKey, r.Header.Get("X-API-KEY"))
				_, _ = w.Write([]byte(`ok`))
			}))

			defer server.Close()

			input := []byte(fmt.Sprintf(`{"method":"GET","url":"%s","headers":{"Authorization":"%s","X-API-KEY":"%s"}}`, server.URL, authorization, xApiKey))
			pair := resolve.NewBufPair()
			err := source.Load(context.Background(), input, pair)
			assert.NoError(t, err)
			assert.Equal(t, string(input), pair.Data.String())
		})
		t.Run("post with body", func(t *testing.T) {

			body := `{"foo":"bar"}`

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				actualBody, err := ioutil.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.Equal(t, string(actualBody), body)
				_, _ = w.Write([]byte(`ok`))
			}))

			defer server.Close()

			input := []byte(fmt.Sprintf(`{"method":"POST","url":"%s","body":%s}`, server.URL, body))
			pair := resolve.NewBufPair()
			err := source.Load(context.Background(), input, pair)
			assert.NoError(t, err)
			assert.Equal(t, string(input), pair.Data.String())
		})
	}

	t.Run("amqp", func(t *testing.T) {
		conn, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		go echoServer(conn)
		time.Sleep(time.Second * 3)

		source := &Source{
			conn: conn,
		}
		runTests(t, source)
	})
}

func echoServer(conn *amqp.Connection) {
	ch, _ := conn.Channel()
	defer ch.Close()

	q, _ := ch.QueueDeclare(
		"rpc_queue", // name
		false,       // durable
		false,       // delete when unused
		false,       // exclusive
		false,       // no-wait
		nil,         // arguments
	)
	_ = ch.Qos(
		1,     // prefetch count
		0,     // prefetch size
		false, // global
	)

	msgs, _ := ch.Consume(
		q.Name, // queue
		"",     // consumer
		false,  // auto-ack
		false,  // exclusive
		false,  // no-local
		false,  // no-wait
		nil,    // args
	)

	forever := make(chan bool)

	go func() {
		for d := range msgs {
			println("GOT:", string(d.Body))

			_ = ch.Publish(
				"",        // exchange
				d.ReplyTo, // routing key
				false,     // mandatory
				false,     // immediate
				amqp.Publishing{
					ContentType:   "text/plain",
					CorrelationId: d.CorrelationId,
					Body:          d.Body,
				})
			_ = d.Ack(false)
		}
	}()
	<-forever
}
