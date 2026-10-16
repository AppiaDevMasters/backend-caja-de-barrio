package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTheLogKeepsTheHostAndLosesThePassword is the finding an independent
// verification of the peer session raised, and it is right: the detail of an
// unclassified failure has to be visible somewhere, and the log is that place,
// but the text of that failure comes from a driver or a caller and this backend
// is the only holder of DATABASE_URL and of the provider keys.
func TestTheLogKeepsTheHostAndLosesThePassword(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	Fail(httptest.NewRecorder(),
		errors.New("dial tcp 10.10.0.4:5432: connection refused, dsn postgres://rafiki:sup3r-s3creto@postgres"))

	if strings.Contains(logged.String(), "sup3r-s3creto") {
		t.Errorf("the log carries the password: %s", logged.String())
	}
	// The point of logging is that a maintainer can act on it, so the host and the
	// port have to survive the redaction.
	if !strings.Contains(logged.String(), "10.10.0.4:5432") {
		t.Errorf("the log lost the host and port a maintainer needs: %s", logged.String())
	}
}

func TestRedactMasksTheShapesThisProjectUses(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
	}{
		"a dsn with a password": {
			in:   "postgres://rafiki:sup3r-s3creto@postgres:5432/rafiki_backend",
			want: "postgres://rafiki:***@postgres:5432/rafiki_backend",
		},
		"an https url with a password": {
			in:   "failed to reach https://admin:letmein@example.com/graphql",
			want: "failed to reach https://admin:***@example.com/graphql",
		},
		"a dsn with a user and no password": {
			in:   "postgres://rafiki@postgres:5432/db",
			want: "postgres://rafiki@postgres:5432/db",
		},
		"a key and value pair": {
			in:   "connect failed password=sup3r-s3creto host=postgres",
			want: "connect failed password=*** host=postgres",
		},
		"an api key assignment": {
			in:   "the provider refused api_key: sk-live-1234567890",
			want: "the provider refused api_key=***",
		},
		"a bearer token": {
			in:   "the provider answered 401 to Bearer eyJhbGciOiJIUzI1NiJ9.abc.def",
			want: "the provider answered 401 to Bearer ***",
		},
		"a plain message with no secret": {
			in:   "the number of open connections reached its limit",
			want: "the number of open connections reached its limit",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := redact(tc.in); got != tc.want {
				t.Errorf("redact(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
			}
		})
	}
}
