package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wundergraph/graphql-go-tools/execution/graphql"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// TestExecutionEngine_FieldAuthorization is the executed-truth check for pre-fetch field
// authorization (@authenticated/@requiresScopes compile to FieldConfiguration.HasAuthorizationRule):
// a denied protected field must come back UNAUTHORIZED, never resolve silently. Unlike the cost
// variant of this suite it sets no cost flags, so under PLANV2=1 the plan is produced by planner-v2
// itself (no fallback; verified via PLANV2_TRACE) -- pre-M4.1 this test FAILED under PLANV2=1
// because planv2 emitted no FieldInfo/RootFields, postprocess collected no authorization
// coordinates, and the secret resolved with no error (PARITY.md Section 6, the register's loudest row).
//
// The subgraph is a real httptest server (not a stubbed factory http.Client): planv2's fetches run
// on http.DefaultClient (the documented transport.go client limitation, M4.6), so a
// transport-stubbed client would only reach the v1 path.
func TestExecutionEngine_FieldAuthorization(t *testing.T) {
	t.Parallel()

	schemaSDL := `
		schema { query: Query }
		type Query {
			user: User
		}
		type User {
			id: ID!
			secret: String
		}
	`
	schema, err := graphql.NewSchemaFromString(schemaSDL)
	require.NoError(t, err)

	subgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"user":{"id":"1","secret":"s3cr3t"}}}`))
	}))
	t.Cleanup(subgraph.Close)

	rootNodes := []plan.TypeField{
		{TypeName: "Query", FieldNames: []string{"user"}},
		{TypeName: "User", FieldNames: []string{"id", "secret"}},
	}
	customConfig := mustConfiguration(t, graphql_datasource.ConfigurationInput{
		Fetch: &graphql_datasource.FetchConfiguration{
			URL: subgraph.URL,
		},
		SchemaConfiguration: mustSchemaConfig(t, nil, schemaSDL),
	})
	fields := []plan.FieldConfiguration{
		{TypeName: "User", FieldName: "secret", HasAuthorizationRule: true},
	}

	dataSources := []plan.DataSource{
		mustGraphqlDataSourceConfiguration(t, "ds-id",
			mustFactory(t, http.DefaultClient),
			&plan.DataSourceMetadata{RootNodes: rootNodes},
			customConfig,
		),
	}

	t.Run("denied protected field resolves UNAUTHORIZED, not silently", runWithoutError(
		ExecutionEngineTestCase{
			schema: schema,
			operation: func(t *testing.T) graphql.Request {
				return graphql.Request{Query: `{ user { id secret } }`}
			},
			dataSources: dataSources,
			fields:      fields,
			engineOptions: []ExecutionOptions{
				WithPreFetchFieldAuthorizer(&denyCoordinatesAuthorizer{denied: map[resolve.GraphCoordinate]string{
					{TypeName: "User", FieldName: "secret"}: "missing scope 'secret:read'",
				}}),
			},
			expectedResponse: `{"errors":[{"message":"Unauthorized to load field 'Query.user.secret', Reason: missing scope 'secret:read'.","path":["user","secret"],"extensions":{"code":"UNAUTHORIZED_FIELD_OR_TYPE"}}],"data":{"user":{"id":"1","secret":null}}}`,
		},
	))

	t.Run("allowed protected field resolves normally", runWithoutError(
		ExecutionEngineTestCase{
			schema: schema,
			operation: func(t *testing.T) graphql.Request {
				return graphql.Request{Query: `{ user { id secret } }`}
			},
			dataSources: dataSources,
			fields:      fields,
			engineOptions: []ExecutionOptions{
				WithPreFetchFieldAuthorizer(&denyCoordinatesAuthorizer{denied: nil}),
			},
			expectedResponse: `{"data":{"user":{"id":"1","secret":"s3cr3t"}}}`,
		},
	))
}
