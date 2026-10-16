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
//
// The resource pages give each type one input, through the field component of
// the web pack:
//
//	Type     Input                          Nullable input
//	string   text input                     text input, empty sends null
//	text     textarea                       textarea, empty sends null
//	int      number input, step 1           number input, empty sends null
//	bigint   number input, step 1           number input, empty sends null
//	bool     checkbox                       native select: not set, yes, no
//	float    number input, any step         number input, empty sends null
//	decimal  text input, decimal keyboard   text input, empty sends null
//	time     datetime-local input, in UTC   datetime-local input, empty sends null
//	date     date input                     date input, empty sends null
//	uuid     text input                     text input, empty sends null
//	json     textarea, a JSON object        textarea, empty sends null
//	ref      number input for the id        number input, empty sends null
//	enum     native select                  native select with "None"
package fields
