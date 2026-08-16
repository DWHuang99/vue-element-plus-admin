package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayRoutesDepartmentAndIAMPaths(t *testing.T) {
	iam := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Upstream", "iam")
		writer.WriteHeader(http.StatusNoContent)
	})
	department := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Upstream", "department")
		writer.WriteHeader(http.StatusNoContent)
	})
	router := newGatewayRouter(iam, department)

	tests := []struct {
		path     string
		upstream string
	}{
		{path: "/api/v1/departments", upstream: "department"},
		{path: "/api/v1/departments/tree", upstream: "department"},
		{path: "/api/v1/users", upstream: "iam"},
		{path: "/.well-known/jwks.json", upstream: "iam"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", recorder.Code)
			}
			if actual := recorder.Header().Get("X-Upstream"); actual != test.upstream {
				t.Fatalf("upstream = %q, want %q", actual, test.upstream)
			}
		})
	}
}

func TestLoadGatewayConfigUsesEnvironment(t *testing.T) {
	t.Setenv("IAM_SERVICE_URL", "http://iam.internal:9000")
	t.Setenv("DEPARTMENT_SERVICE_URL", "http://department.internal:9000")
	t.Setenv("GATEWAY_DIAL_TIMEOUT_MS", "250")
	configuration, err := loadGatewayConfig()
	if err != nil {
		t.Fatalf("loadGatewayConfig() error = %v", err)
	}
	if configuration.iamServiceURL != "http://iam.internal:9000" {
		t.Fatalf("IAM URL = %q", configuration.iamServiceURL)
	}
	if configuration.departmentServiceURL != "http://department.internal:9000" {
		t.Fatalf("Department URL = %q", configuration.departmentServiceURL)
	}
	if configuration.dialTimeout.Milliseconds() != 250 {
		t.Fatalf("dial timeout = %s, want 250ms", configuration.dialTimeout)
	}
}
