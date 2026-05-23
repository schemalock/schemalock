package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestFetchVersions(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		status    int
		body      string
		want      []string
		wantErr   bool
		wantNFErr bool
	}{
		{
			name:   "200 valid array",
			path:   "/kubernetes/operator.victoriametrics.com/versions.json",
			status: http.StatusOK,
			body:   `["0.70.0","0.68.4"]`,
			want:   []string{"0.70.0", "0.68.4"},
		},
		{
			name:   "200 empty array",
			path:   "/kubernetes/empty.example.com/versions.json",
			status: http.StatusOK,
			body:   `[]`,
			want:   []string{},
		},
		{
			name:      "404 not found",
			path:      "/kubernetes/unknown.example.com/versions.json",
			status:    http.StatusNotFound,
			body:      "",
			wantErr:   true,
			wantNFErr: true,
		},
		{
			name:    "500 server error",
			path:    "/kubernetes/broken.example.com/versions.json",
			status:  http.StatusInternalServerError,
			body:    "boom",
			wantErr: true,
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, tc := range tests {
			if r.URL.Path == tc.path {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			group := groupFromPath(tc.path)
			got, err := c.FetchVersions(ctx, "kubernetes", group)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantNFErr && !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want errors.Is ErrNotFound", err)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// groupFromPath extracts the group segment from "/kubernetes/<group>/versions.json".
func groupFromPath(p string) string {
	// trivial parser: split on "/"
	count := 0
	cur := ""
	out := ""
	for _, r := range p {
		if r == '/' {
			if count == 2 {
				out = cur
			}
			count++
			cur = ""
			continue
		}
		cur += string(r)
	}
	return out
}
