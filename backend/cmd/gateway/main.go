package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type gatewayConfig struct {
	address               string
	iamServiceURL         string
	departmentServiceURL  string
	dialTimeout           time.Duration
	responseHeaderTimeout time.Duration
	readHeaderTimeout     time.Duration
	readTimeout           time.Duration
	writeTimeout          time.Duration
	idleTimeout           time.Duration
}

func main() {
	configuration, err := loadGatewayConfig()
	if err != nil {
		log.Fatalf("load gateway configuration: %v", err)
	}
	iamProxy, err := newProxy(configuration.iamServiceURL, configuration)
	if err != nil {
		log.Fatalf("configure IAM proxy: %v", err)
	}
	departmentProxy, err := newProxy(configuration.departmentServiceURL, configuration)
	if err != nil {
		log.Fatalf("configure Department proxy: %v", err)
	}

	router := newGatewayRouter(iamProxy, departmentProxy)

	server := &http.Server{
		Addr:              configuration.address,
		Handler:           router,
		ReadHeaderTimeout: configuration.readHeaderTimeout,
		ReadTimeout:       configuration.readTimeout,
		WriteTimeout:      configuration.writeTimeout,
		IdleTimeout:       configuration.idleTimeout,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("run gateway: %v", err)
	}
}

func newGatewayRouter(iamHandler, departmentHandler http.Handler) *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())
	router.GET("/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"message": "pong"}) })
	router.NoRoute(func(c *gin.Context) {
		if c.Request.URL.Path == "/api/v1/departments" ||
			strings.HasPrefix(c.Request.URL.Path, "/api/v1/departments/") {
			departmentHandler.ServeHTTP(c.Writer, c.Request)
			return
		}
		iamHandler.ServeHTTP(c.Writer, c.Request)
	})
	return router
}

func newProxy(target string, configuration *gatewayConfig) (*httputil.ReverseProxy, error) {
	targetURL, err := url.Parse(target)
	if err != nil || targetURL.Scheme == "" || targetURL.Host == "" {
		return nil, fmt.Errorf("invalid service URL %q", target)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   configuration.dialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.ResponseHeaderTimeout = configuration.responseHeaderTimeout
	transport.TLSHandshakeTimeout = configuration.dialTimeout

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = transport
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
		log.Printf("proxy request failed: method=%s path=%s error=%v", request.Method, request.URL.Path, proxyErr)
		http.Error(writer, "upstream service unavailable", http.StatusBadGateway)
	}
	return proxy, nil
}

func loadGatewayConfig() (*gatewayConfig, error) {
	duration := func(key string, fallback int64) (time.Duration, error) {
		value := env(key, strconv.FormatInt(fallback, 10))
		milliseconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || milliseconds <= 0 {
			return 0, fmt.Errorf("%s must be a positive integer", key)
		}
		return time.Duration(milliseconds) * time.Millisecond, nil
	}
	dialTimeout, err := duration("GATEWAY_DIAL_TIMEOUT_MS", 1000)
	if err != nil {
		return nil, err
	}
	responseHeaderTimeout, err := duration("GATEWAY_RESPONSE_HEADER_TIMEOUT_MS", 3000)
	if err != nil {
		return nil, err
	}
	readHeaderTimeout, err := duration("GATEWAY_READ_HEADER_TIMEOUT_MS", 2000)
	if err != nil {
		return nil, err
	}
	readTimeout, err := duration("GATEWAY_READ_TIMEOUT_MS", 10000)
	if err != nil {
		return nil, err
	}
	writeTimeout, err := duration("GATEWAY_WRITE_TIMEOUT_MS", 10000)
	if err != nil {
		return nil, err
	}
	idleTimeout, err := duration("GATEWAY_IDLE_TIMEOUT_MS", 60000)
	if err != nil {
		return nil, err
	}
	return &gatewayConfig{
		address:               env("GATEWAY_ADDR", ":8080"),
		iamServiceURL:         env("IAM_SERVICE_URL", "http://localhost:8081"),
		departmentServiceURL:  env("DEPARTMENT_SERVICE_URL", "http://localhost:8082"),
		dialTimeout:           dialTimeout,
		responseHeaderTimeout: responseHeaderTimeout,
		readHeaderTimeout:     readHeaderTimeout,
		readTimeout:           readTimeout,
		writeTimeout:          writeTimeout,
		idleTimeout:           idleTimeout,
	}, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
