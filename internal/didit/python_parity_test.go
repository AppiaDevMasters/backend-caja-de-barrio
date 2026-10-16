package didit

import "testing"

// These are the cases where Go's own JSON encoder and the canonical form Didit
// signs disagree, so getting them wrong produces a valid delivery that the
// receiver rejects with a silent 401.
//
// Every expected value in this table was produced by running the documented
// canonicalizer:
//
//	python3 -c 'import json; v=json.loads(INPUT); print(json.dumps(v, sort_keys=True, separators=(",",":"), ensure_ascii=False))'
//
// after applying the provider's shorten_floats, which turns a whole-valued float
// into an int. They are measured values, not guesses.
func TestCanonicalJSONMatchesTheDocumentedPythonForm(t *testing.T) {
	cases := map[string]struct {
		input string
		want  string
	}{
		// Float notation. Python writes a float positionally while its decimal
		// exponent stays in [-4, 16) and in exponent form outside it, while Go's
		// %g switches to exponent form at 6. Nothing about this is optional: the
		// bytes are the signature.
		"fraction above the Go %g threshold": {input: `{"score":1234567.5}`, want: `{"score":1234567.5}`},
		"fraction just below it":             {input: `{"score":999999.5}`, want: `{"score":999999.5}`},
		"fraction just below 1e6":            {input: `{"score":123456.5}`, want: `{"score":123456.5}`},
		"fraction inside the range":          {input: `{"score":0.94}`, want: `{"score":0.94}`},
		"small magnitude positional":         {input: `{"score":1e-4}`, want: `{"score":0.0001}`},
		"small magnitude in exponent form":   {input: `{"score":1e-5}`, want: `{"score":1e-05}`},

		// A whole-valued float becomes an int, and Python's int is exact: it is
		// the binary value the float holds, not the shortest decimal that
		// round-trips back to it. The two differ from 1e23 upwards.
		"whole float exactly representable":       {input: `{"n":1e15}`, want: `{"n":1000000000000000}`},
		"whole float above the exact int range":   {input: `{"n":1e16}`, want: `{"n":10000000000000000}`},
		"whole float whose binary value is dense": {input: `{"n":1e30}`, want: `{"n":1000000000000000019884624838656}`},

		// A literal integer keeps its digits, except that Python has no negative
		// zero: json.loads("-0") is the int 0.
		"negative zero literal": {input: `{"n":-0}`, want: `{"n":0}`},
		"negative zero float":   {input: `{"n":-0.0}`, want: `{"n":0}`},
		"identifier past 2^53":  {input: `{"n":12345678901234567890}`, want: `{"n":12345678901234567890}`},

		// Two control characters have a short escape in Python and a \uXXXX escape
		// in Go. The rest of the control range uses \u00xx on both sides.
		"backspace":    {input: `{"a":"\b"}`, want: `{"a":"\b"}`},
		"form feed":    {input: `{"a":"\f"}`, want: `{"a":"\f"}`},
		"newline":      {input: `{"a":"\n"}`, want: `{"a":"\n"}`},
		"tab":          {input: `{"a":"\t"}`, want: `{"a":"\t"}`},
		"null":         {input: `{"a":"\u0000"}`, want: `{"a":"\u0000"}`},
		"unit sep":     {input: `{"a":"\u001f"}`, want: `{"a":"\u001f"}`},
		"vertical tab": {input: `{"a":"\u000b"}`, want: `{"a":"\u000b"}`},
		// 0x7f is the exception in this range: with ensure_ascii=False Python
		// leaves it literal, and Go does too, so the canonical form carries the
		// raw byte rather than an escape.
		"delete stays literal": {input: `{"a":"\u007f"}`, want: "{\"a\":\"\u007f\"}"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := canonicalJSON([]byte(tc.input))
			if err != nil {
				t.Fatalf("canonicalJSON(%s) error = %v, want nil", tc.input, err)
			}
			if string(got) != tc.want {
				t.Errorf("canonicalJSON(%s)\n  got  %s\n  want %s", tc.input, got, tc.want)
			}
		})
	}
}

// TestCanonicalJSONRefusesASecondDocument pins the branch the first round of
// tests never reached: a body with a second JSON document appended would sit
// outside what the sender signed.
func TestCanonicalJSONRefusesASecondDocument(t *testing.T) {
	for _, input := range []string{`{"a":1} {"b":2}`, `{"a":1}{`, "{}{}", `{"a":1} null`} {
		if _, err := canonicalJSON([]byte(input)); err == nil {
			t.Errorf("canonicalJSON(%q) error = nil, want a failure", input)
		}
	}
}
