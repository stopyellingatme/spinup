// Package fields parses resource fields and maps field types across generated layers.
//
// Types returns the stable table below. A nullable field has Nullable set to true.
//
//	Type     SQL               Go store               Go API                       OpenAPI             TypeScript
//	string   text              string                 string                       string              string
//	text     text              string                 string                       string              string
//	int      integer           int32                  int32                        integer/int32       number
//	bigint   bigint            int64                  int64                        integer/int64       number
//	bool     boolean           bool                   bool                         boolean             boolean
//	float    double precision  float64                float64                      number/double       number
//	decimal  numeric           string                 string                       string/decimal      string
//	time     timestamptz       time.Time              time.Time                    string/date-time    string
//	date     date              time.Time              openapi_types.Date           string/date         string
//	uuid     uuid              uuid.UUID              uuid.UUID                    string/uuid         string
//	json     jsonb             []byte                 map[string]interface{}       object              Record<string, unknown>
//	ref      bigint            int64                  int64                        integer/int64       number
//	enum     text              string                 string                       string              enum union
package fields
