package syslog_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"code.cloudfoundry.org/go-loggregator/rpc/loggregator_v2"
	"code.cloudfoundry.org/go-metric-registry/testhelpers"
	"code.cloudfoundry.org/log-cache/internal/syslog"
	"code.cloudfoundry.org/log-cache/internal/testing"
	"code.cloudfoundry.org/log-cache/pkg/rpc/logcache_v1"
	"code.cloudfoundry.org/tlsconfig"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
)

const defaultLogMessage = `145 <14>1 1970-01-01T00:00:00.012345+00:00 test-hostname test-app-id [APP/2] - [tags@47450 key="value" source_type="actual-source-type"] just a test` + "\n"

var _ = Describe("Syslog", func() {
	var (
		server     *syslog.Server
		spyMetrics *testhelpers.SpyMetricsRegistry
		logCache   *spyLogCacheClient
		loggr      *log.Logger
	)

	BeforeEach(func() {
		spyMetrics = testhelpers.NewMetricsRegistry()
		logCache = newSpyLogCacheClient()
		loggr = log.New(GinkgoWriter, "", log.LstdFlags)

		server = syslog.NewServer(
			loggr,
			logCache,
			spyMetrics,
			testing.LogCacheTestCerts.Cert("log-cache"),
			testing.LogCacheTestCerts.Key("log-cache"),
			syslog.WithServerPort(0),
		)

		go server.Start()
		waitForServerToStart(server)
	})

	AfterEach(func() {
		server.Stop()
	})

	It("closes connection after idle timeout", func() {
		timeoutServer := syslog.NewServer(
			loggr,
			logCache,
			spyMetrics,
			testing.LogCacheTestCerts.Cert("log-cache"),
			testing.LogCacheTestCerts.Key("log-cache"),
			syslog.WithServerPort(0),
			syslog.WithIdleTimeout(100*time.Millisecond),
		)

		go timeoutServer.Start()
		waitForServerToStart(timeoutServer)
		defer timeoutServer.Stop()

		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(timeoutServer.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		readErrs := make(chan error, 1)
		go func() {
			_, err = conn.Read(make([]byte, 1024))
			readErrs <- err
		}()

		Eventually(readErrs).Should(Receive(MatchError(io.EOF)))
	})

	It("keeps the connection open if writes are occurring", func() {
		timeoutServer := syslog.NewServer(
			loggr,
			logCache,
			spyMetrics,
			testing.LogCacheTestCerts.Cert("log-cache"),
			testing.LogCacheTestCerts.Key("log-cache"),
			syslog.WithServerPort(0),
			syslog.WithIdleTimeout(100*time.Millisecond),
		)

		go timeoutServer.Start()
		waitForServerToStart(timeoutServer)

		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(timeoutServer.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		Consistently(func() error {
			_, err = fmt.Fprint(conn, defaultLogMessage)
			return err
		}, 1).Should(Succeed())
	})

	It("counts incoming messages", func() {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, defaultLogMessage)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, defaultLogMessage)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return spyMetrics.HasMetric("ingress", nil)
		}).Should(BeTrue())

		Eventually(func() float64 {
			return spyMetrics.GetMetric("ingress", nil).Value()
		}).Should(Equal(2.0))
	})

	It("sends log messages to log cache", func() {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, defaultLogMessage)
		Expect(err).ToNot(HaveOccurred())

		Eventually(logCache.envelopes).Should(ContainElement(
			&loggregator_v2.Envelope{
				Tags: map[string]string{
					"source_type": "actual-source-type",
					"key":         "value",
				},
				InstanceId: "2",
				Timestamp:  12345000,
				SourceId:   "test-app-id",
				Message: &loggregator_v2.Envelope_Log{
					Log: &loggregator_v2.Log{
						Payload: []byte("just a test"),
						Type:    loggregator_v2.Log_OUT,
					},
				},
			},
		))
	})

	It("sends log messages to log cache", func() {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, defaultLogMessage)
		Expect(err).ToNot(HaveOccurred())

		Eventually(logCache.envelopes).Should(ContainElement(
			&loggregator_v2.Envelope{
				Tags: map[string]string{
					"source_type": "actual-source-type",
					"key":         "value",
				},
				InstanceId: "2",
				Timestamp:  12345000,
				SourceId:   "test-app-id",
				Message: &loggregator_v2.Envelope_Log{
					Log: &loggregator_v2.Log{
						Payload: []byte("just a test"),
						Type:    loggregator_v2.Log_OUT,
					},
				},
			},
		))
	})

	It("sends counter messages to log cache", func() {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, buildSyslogWithTags(fmt.Sprintf(counterDataFormat, "some-counter", "99", "1"), `key="value"`))
		Expect(err).ToNot(HaveOccurred())

		Eventually(logCache.envelopes).Should(ContainElement(
			&loggregator_v2.Envelope{
				InstanceId: "1",
				Timestamp:  12345000,
				SourceId:   "test-app-id",
				Tags: map[string]string{
					"key": "value",
				},
				Message: &loggregator_v2.Envelope_Counter{
					Counter: &loggregator_v2.Counter{
						Name:  "some-counter",
						Delta: 1,
						Total: 99,
					},
				},
			},
		))
	})

	It("sends gauge messages to log cache", func() {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, buildSyslog(fmt.Sprintf(gaugeDataFormat, "cpu", "0.23", `unit="percentage"`)))
		Expect(err).ToNot(HaveOccurred())

		Eventually(logCache.envelopes).Should(ContainElement(
			&loggregator_v2.Envelope{
				InstanceId: "1",
				Timestamp:  12345000,
				SourceId:   "test-app-id",
				Tags:       map[string]string{},
				Message: &loggregator_v2.Envelope_Gauge{
					Gauge: &loggregator_v2.Gauge{
						Metrics: map[string]*loggregator_v2.GaugeValue{
							"cpu": {Unit: "percentage", Value: 0.23},
						},
					},
				},
			},
		))
	})

	It("sends event messages to log cache", func() {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, buildSyslog(`event@47450 title="event-title" body="event-body"`))
		Expect(err).ToNot(HaveOccurred())

		Eventually(logCache.envelopes).Should(ContainElement(
			&loggregator_v2.Envelope{
				InstanceId: "1",
				Timestamp:  12345000,
				SourceId:   "test-app-id",
				Tags:       map[string]string{},
				Message: &loggregator_v2.Envelope_Event{
					Event: &loggregator_v2.Event{
						Title: "event-title",
						Body:  "event-body",
					},
				},
			},
		))
	})

	It("sends timer messages to log cache", func() {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, buildSyslog(`timer@47450 name="some-name" start="10" stop="20"`))
		Expect(err).ToNot(HaveOccurred())

		Eventually(logCache.envelopes).Should(ContainElement(
			&loggregator_v2.Envelope{
				InstanceId: "1",
				Timestamp:  12345000,
				SourceId:   "test-app-id",
				Tags:       map[string]string{},
				Message: &loggregator_v2.Envelope_Timer{
					Timer: &loggregator_v2.Timer{
						Name:  "some-name",
						Start: 10,
						Stop:  20,
					},
				},
			},
		))
	})

	It("increments invalid message metric when there is an invalid syslog message", func() {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		lengthIsWrong := "126 <14>1 1970-01-01T00:00:00.012345+00:00 test-hostname test-app-id [1] - [gauge@47450 name=\"cpu\" value=\"0.23\" unit=\"percentage\"] \n"
		_, err = fmt.Fprint(conn, lengthIsWrong)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, "128 <14>1 1970-01-01T00:00:00.012345+00:00 test-hostname test-app-id [1] - [gauge@47450 name=\"cpu\" value=\"0.23\" unit=\"percentage\"] \n")
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return spyMetrics.HasMetric("invalid_ingress", nil)
		}).Should(BeTrue())

		Eventually(func() float64 {
			return spyMetrics.GetMetric("invalid_ingress", nil).Value()
		}).Should(Equal(1.0))

		Eventually(func() bool {
			return spyMetrics.HasMetric("ingress", nil)
		}).Should(BeTrue())

		Eventually(func() float64 {
			return spyMetrics.GetMetric("ingress", nil).Value()
		}).Should(Equal(1.0))
	})

	It("increments invalid message metric when there is missing data", func() {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		noAppName := "79 <14>1 1970-01-01T00:00:00.012345+00:00 test-hostname - [APP/2] - - just a test\n"
		_, err = fmt.Fprint(conn, noAppName)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() float64 {
			return spyMetrics.GetMetricValue("invalid_ingress", nil)
		}).Should(Equal(1.0))

		noProcID := "83 <14>1 1970-01-01T00:00:00.012345+00:00 test-hostname test-app-id - - - just a test\n"
		_, err = fmt.Fprint(conn, noProcID)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() float64 {
			return spyMetrics.GetMetricValue("invalid_ingress", nil)
		}).Should(Equal(2.0))

		noMessage := "76 <14>1 1970-01-01T00:00:00.012345+00:00 test-hostname test-app-id [APP/2] - -"
		_, err = fmt.Fprint(conn, noMessage)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() float64 {
			return spyMetrics.GetMetricValue("invalid_ingress", nil)
		}).Should(Equal(3.0))

		noPriority := "88 <->1 1970-01-01T00:00:00.012345+00:00 test-hostname test-app-id [APP/2] - - just a test\n"
		_, err = fmt.Fprint(conn, noPriority)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() float64 {
			return spyMetrics.GetMetricValue("invalid_ingress", nil)
		}).Should(Equal(4.0))
	})

	It("does not send invalid envelopes to log cache", func() {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, buildSyslog(buildSyslog(fmt.Sprintf(counterDataFormat, "some-counter", "99", "d"))))

		Consistently(logCache.envelopes).Should(HaveLen(0))
	})

	DescribeTable("increments invalid ingress on invalid envelope data", func(rfc5424Log string) {
		tlsConfig := buildClientTLSConfig(tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
		conn, err := tlsClientConnection(server.Addr(), tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		_, err = fmt.Fprint(conn, rfc5424Log)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return spyMetrics.HasMetric("invalid_ingress", nil)
		}).Should(BeTrue())

		Eventually(func() float64 {
			return spyMetrics.GetMetric("invalid_ingress", nil).Value()
		}).Should(Equal(1.0))
	},
		Entry("Counter - invalid delta", buildSyslog(fmt.Sprintf(counterDataFormat, "some-counter", "99", "d"))),
		Entry("Counter - invalid total", buildSyslog(fmt.Sprintf(counterDataFormat, "some-counter", "dd", "9"))),
		Entry("Gauge - no unit provided", buildSyslog(fmt.Sprintf(gaugeDataFormat, "cpu", "0.23", "blah=\"percentage\""))),
		Entry("Gauge - invalid value", buildSyslog(fmt.Sprintf(gaugeDataFormat, "cpu", "dddd", "unit=\"percentage\""))),
	)

	Describe("TLS security", func() {
		DescribeTable("allows only supported TLS versions", func(clientTLSVersion int, serverShouldAllow bool) {
			tlsConfig := buildClientTLSConfig(uint16(clientTLSVersion), tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256)
			_, err := tlsClientConnection(server.Addr(), tlsConfig)

			if serverShouldAllow {
				Expect(err).ToNot(HaveOccurred())
			} else {
				Expect(err).To(HaveOccurred())
			}
		},
			Entry("unsupported SSL 3.0", tls.VersionSSL30, false),
			Entry("unsupported TLS 1.0", tls.VersionTLS10, false),
			Entry("unsupported TLS 1.1", tls.VersionTLS11, false),
			Entry("supported TLS 1.2", tls.VersionTLS12, true),
		)

		DescribeTable("allows only supported TLS versions", func(cipherSuite uint16, serverShouldAllow bool) {
			tlsConfig := buildClientTLSConfig(tls.VersionTLS12, cipherSuite)
			_, err := tlsClientConnection(server.Addr(), tlsConfig)

			if serverShouldAllow {
				Expect(err).ToNot(HaveOccurred())
			} else {
				Expect(err).To(HaveOccurred())
			}
		},
			Entry("unsupported cipher RSA_WITH_3DES_EDE_CBC_SHA", tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA, false),
			Entry("unsupported cipher ECDHE_RSA_WITH_3DES_EDE_CBC_SHA", tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA, false),
			Entry("unsupported cipher RSA_WITH_RC4_128_SHA", tls.TLS_RSA_WITH_RC4_128_SHA, false),
			Entry("unsupported cipher RSA_WITH_AES_128_CBC_SHA256", tls.TLS_RSA_WITH_AES_128_CBC_SHA256, false),
			Entry("unsupported cipher ECDHE_ECDSA_WITH_CHACHA20_POLY1305", tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305, false),
			Entry("unsupported cipher ECDHE_ECDSA_WITH_RC4_128_SHA", tls.TLS_ECDHE_ECDSA_WITH_RC4_128_SHA, false),
			Entry("unsupported cipher ECDHE_ECDSA_WITH_AES_128_CBC_SHA", tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA, false),
			Entry("unsupported cipher ECDHE_ECDSA_WITH_AES_256_CBC_SHA", tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA, false),
			Entry("unsupported cipher ECDHE_ECDSA_WITH_AES_128_CBC_SHA256", tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256, false),
			Entry("unsupported cipher ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, false),
			Entry("unsupported cipher ECDHE_ECDSA_WITH_AES_256_GCM_SHA384", tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, false),
			Entry("unsupported cipher ECDHE_RSA_WITH_RC4_128_SHA", tls.TLS_ECDHE_RSA_WITH_RC4_128_SHA, false),
			Entry("unsupported cipher ECDHE_RSA_WITH_AES_128_CBC_SHA256", tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256, false),
			Entry("unsupported cipher ECDHE_RSA_WITH_AES_128_CBC_SHA", tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA, false),
			Entry("unsupported cipher ECDHE_RSA_WITH_AES_256_CBC_SHA", tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA, false),
			Entry("unsupported cipher ECDHE_RSA_WITH_CHACHA20_POLY1305", tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305, false),
			Entry("unsupported cipher RSA_WITH_AES_128_CBC_SHA", tls.TLS_RSA_WITH_AES_128_CBC_SHA, false),
			Entry("unsupported cipher RSA_WITH_AES_128_GCM_SHA256", tls.TLS_RSA_WITH_AES_128_GCM_SHA256, false),
			Entry("unsupported cipher RSA_WITH_AES_256_CBC_SHA", tls.TLS_RSA_WITH_AES_256_CBC_SHA, false),
			Entry("unsupported cipher RSA_WITH_AES_256_GCM_SHA384", tls.TLS_RSA_WITH_AES_256_GCM_SHA384, false),

			Entry("supported cipher ECDHE_RSA_WITH_AES_128_GCM_SHA256", tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, true),
			Entry("supported cipher ECDHE_RSA_WITH_AES_256_GCM_SHA384", tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384, true),
		)
	})
})

func buildSyslog(structuredData string) string {
	msg := fmt.Sprintf("<14>1 1970-01-01T00:00:00.012345+00:00 test-hostname test-app-id [1] - [%s] \n", structuredData)
	return fmt.Sprintf("%d %s", len(msg), msg)
}

func buildSyslogWithTags(structuredData, tags string) string {
	msg := fmt.Sprintf("<14>1 1970-01-01T00:00:00.012345+00:00 test-hostname test-app-id [1] - [%s][tags@47450 %s] \n", structuredData, tags)
	return fmt.Sprintf("%d %s", len(msg), msg)
}

func waitForServerToStart(server *syslog.Server) {
	Eventually(server.Addr, "1s", "100ms").ShouldNot(BeEmpty())
}

const counterDataFormat = `counter@47450 name="%s" total="%s" delta="%s"`
const gaugeDataFormat = `gauge@47450 name="%s" value="%s" %s`

func newSpyLogCacheClient() *spyLogCacheClient {
	return &spyLogCacheClient{}
}

type spyLogCacheClient struct {
	sync.Mutex
	envs      []*loggregator_v2.Envelope
	sendError error
}

func (s *spyLogCacheClient) Send(ctx context.Context, in *logcache_v1.SendRequest, opts ...grpc.CallOption) (*logcache_v1.SendResponse, error) {
	s.Lock()
	defer s.Unlock()

	if s.sendError != nil {
		return nil, s.sendError
	}

	s.envs = append(s.envs, in.Envelopes.Batch...)
	return &logcache_v1.SendResponse{}, nil
}

func (s *spyLogCacheClient) envelopes() []*loggregator_v2.Envelope {
	s.Lock()
	defer s.Unlock()

	return s.envs
}

func buildClientTLSConfig(maxVersion, cipherSuite uint16) *tls.Config {
	tlsConf, err := tlsconfig.Build(
		tlsconfig.WithIdentityFromFile(
			testing.LogCacheTestCerts.Cert("log-cache"),
			testing.LogCacheTestCerts.Key("log-cache"),
		),
	).Client()
	Expect(err).ToNot(HaveOccurred())

	tlsConf.MaxVersion = uint16(maxVersion)
	tlsConf.CipherSuites = []uint16{cipherSuite}
	tlsConf.InsecureSkipVerify = true

	return tlsConf
}

func tlsClientConnection(addr string, tlsConf *tls.Config) (*tls.Conn, error) {
	dialer := &net.Dialer{}
	dialer.Timeout = time.Second
	return tls.DialWithDialer(dialer, "tcp", addr, tlsConf)
}
