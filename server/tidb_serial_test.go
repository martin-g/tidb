// Copyright 2021 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build !race
// +build !race

package server

import (
	"context"
	"crypto/x509"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/pingcap/errors"
	"github.com/pingcap/tidb/config"
	tmysql "github.com/pingcap/tidb/parser/mysql"
	"github.com/pingcap/tidb/sessionctx/variable"
	"github.com/pingcap/tidb/testkit"
	"github.com/pingcap/tidb/util"
	"github.com/pingcap/tidb/util/collate"
	"github.com/stretchr/testify/require"
)

// this test will change `kv.TxnTotalSizeLimit` which may affect other test suites,
// so we must make it running in serial.
func TestLoadData(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	ts.runTestLoadData(t, ts.server)
	ts.runTestLoadDataWithSelectIntoOutfile(t, ts.server)
	ts.runTestLoadDataForSlowLog(t, ts.server)
}

func TestConfigDefaultValue(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	ts.runTestsOnNewDB(t, nil, "config", func(dbt *testkit.DBTestKit) {
		rows := dbt.MustQuery("select @@tidb_slow_log_threshold;")
		ts.checkRows(t, rows, "300")
	})
}

// Fix issue#22540. Change tidb_dml_batch_size,
// then check if load data into table with auto random column works properly.
func TestLoadDataAutoRandom(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	ts.runTestLoadDataAutoRandom(t)
}

func TestLoadDataAutoRandomWithSpecialTerm(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	ts.runTestLoadDataAutoRandomWithSpecialTerm(t)
}

func TestExplainFor(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	ts.runTestExplainForConn(t)
}

func TestStmtCount(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	ts.runTestStmtCount(t)
}

func TestLoadDataListPartition(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	ts.runTestLoadDataForListPartition(t)
	ts.runTestLoadDataForListPartition2(t)
	ts.runTestLoadDataForListColumnPartition(t)
	ts.runTestLoadDataForListColumnPartition2(t)
}

func TestTLSAuto(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	// Start the server without TLS configure, letting the server create these as AutoTLS is enabled
	connOverrider := func(config *mysql.Config) {
		config.TLSConfig = "skip-verify"
	}
	cli := newTestServerClient()
	cfg := newTestConfig()
	cfg.Port = cli.port
	cfg.Status.ReportStatus = false
	cfg.Security.AutoTLS = true
	cfg.Security.RSAKeySize = 528 // Reduces unittest runtime
	err := os.MkdirAll(cfg.TempStoragePath, 0700)
	require.NoError(t, err)
	server, err := NewServer(cfg, ts.tidbdrv)
	require.NoError(t, err)
	cli.port = getPortFromTCPAddr(server.listener.Addr())
	go func() {
		err := server.Run()
		require.NoError(t, err)
	}()
	time.Sleep(time.Millisecond * 100)
	err = cli.runTestTLSConnection(t, connOverrider) // Relying on automatically created TLS certificates
	require.NoError(t, err)

	server.Close()
}

func TestTLSBasic(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	// Generate valid TLS certificates.
	caCert, caKey, err := generateCert(0, "TiDB CA", nil, nil, "/tmp/ca-key.pem", "/tmp/ca-cert.pem")
	require.NoError(t, err)
	serverCert, _, err := generateCert(1, "tidb-server", caCert, caKey, "/tmp/server-key.pem", "/tmp/server-cert.pem")
	require.NoError(t, err)
	_, _, err = generateCert(2, "SQL Client Certificate", caCert, caKey, "/tmp/client-key.pem", "/tmp/client-cert.pem")
	require.NoError(t, err)
	err = registerTLSConfig("client-certificate", "/tmp/ca-cert.pem", "/tmp/client-cert.pem", "/tmp/client-key.pem", "tidb-server", true)
	require.NoError(t, err)

	defer func() {
		err := os.Remove("/tmp/ca-key.pem")
		require.NoError(t, err)
		err = os.Remove("/tmp/ca-cert.pem")
		require.NoError(t, err)
		err = os.Remove("/tmp/server-key.pem")
		require.NoError(t, err)
		err = os.Remove("/tmp/server-cert.pem")
		require.NoError(t, err)
		err = os.Remove("/tmp/client-key.pem")
		require.NoError(t, err)
		err = os.Remove("/tmp/client-cert.pem")
		require.NoError(t, err)
	}()

	// Start the server with TLS but without CA, in this case the server will not verify client's certificate.
	connOverrider := func(config *mysql.Config) {
		config.TLSConfig = "skip-verify"
	}
	cli := newTestServerClient()
	cfg := newTestConfig()
	cfg.Port = cli.port
	cfg.Status.ReportStatus = false
	cfg.Security = config.Security{
		SSLCert: "/tmp/server-cert.pem",
		SSLKey:  "/tmp/server-key.pem",
	}
	server, err := NewServer(cfg, ts.tidbdrv)
	require.NoError(t, err)
	cli.port = getPortFromTCPAddr(server.listener.Addr())
	go func() {
		err := server.Run()
		require.NoError(t, err)
	}()
	time.Sleep(time.Millisecond * 100)
	err = cli.runTestTLSConnection(t, connOverrider) // We should establish connection successfully.
	require.NoError(t, err)
	cli.runTestRegression(t, connOverrider, "TLSRegression")
	// Perform server verification.
	connOverrider = func(config *mysql.Config) {
		config.TLSConfig = "client-certificate"
	}
	err = cli.runTestTLSConnection(t, connOverrider) // We should establish connection successfully.
	require.NoError(t, err, "%v", errors.ErrorStack(err))
	cli.runTestRegression(t, connOverrider, "TLSRegression")

	// Test SSL/TLS session vars
	var v *variable.SessionVars
	stats, err := server.Stats(v)
	require.NoError(t, err)
	_, hasKey := stats["Ssl_server_not_after"]
	require.True(t, hasKey)
	_, hasKey = stats["Ssl_server_not_before"]
	require.True(t, hasKey)
	require.Equal(t, serverCert.NotAfter.Format("Jan _2 15:04:05 2006 MST"), stats["Ssl_server_not_after"])
	require.Equal(t, serverCert.NotBefore.Format("Jan _2 15:04:05 2006 MST"), stats["Ssl_server_not_before"])

	server.Close()
}

func TestTLSVerify(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	// Generate valid TLS certificates.
	caCert, caKey, err := generateCert(0, "TiDB CA", nil, nil, "/tmp/ca-key.pem", "/tmp/ca-cert.pem")
	require.NoError(t, err)
	_, _, err = generateCert(1, "tidb-server", caCert, caKey, "/tmp/server-key.pem", "/tmp/server-cert.pem")
	require.NoError(t, err)
	_, _, err = generateCert(2, "SQL Client Certificate", caCert, caKey, "/tmp/client-key.pem", "/tmp/client-cert.pem")
	require.NoError(t, err)
	err = registerTLSConfig("client-certificate", "/tmp/ca-cert.pem", "/tmp/client-cert.pem", "/tmp/client-key.pem", "tidb-server", true)
	require.NoError(t, err)

	defer func() {
		err := os.Remove("/tmp/ca-key.pem")
		require.NoError(t, err)
		err = os.Remove("/tmp/ca-cert.pem")
		require.NoError(t, err)
		err = os.Remove("/tmp/server-key.pem")
		require.NoError(t, err)
		err = os.Remove("/tmp/server-cert.pem")
		require.NoError(t, err)
		err = os.Remove("/tmp/client-key.pem")
		require.NoError(t, err)
		err = os.Remove("/tmp/client-cert.pem")
		require.NoError(t, err)
	}()

	// Start the server with TLS & CA, if the client presents its certificate, the certificate will be verified.
	cli := newTestServerClient()
	cfg := newTestConfig()
	cfg.Port = cli.port
	cfg.Status.ReportStatus = false
	cfg.Security = config.Security{
		SSLCA:   "/tmp/ca-cert.pem",
		SSLCert: "/tmp/server-cert.pem",
		SSLKey:  "/tmp/server-key.pem",
	}
	server, err := NewServer(cfg, ts.tidbdrv)
	require.NoError(t, err)
	cli.port = getPortFromTCPAddr(server.listener.Addr())
	go func() {
		err := server.Run()
		require.NoError(t, err)
	}()
	time.Sleep(time.Millisecond * 100)
	// The client does not provide a certificate, the connection should succeed.
	err = cli.runTestTLSConnection(t, nil)
	require.NoError(t, err)
	connOverrider := func(config *mysql.Config) {
		config.TLSConfig = "client-certificate"
	}
	cli.runTestRegression(t, connOverrider, "TLSRegression")
	// The client provides a valid certificate.
	connOverrider = func(config *mysql.Config) {
		config.TLSConfig = "client-certificate"
	}
	err = cli.runTestTLSConnection(t, connOverrider)
	require.NoError(t, err)
	cli.runTestRegression(t, connOverrider, "TLSRegression")
	server.Close()

	require.False(t, util.IsTLSExpiredError(errors.New("unknown test")))
	require.False(t, util.IsTLSExpiredError(x509.CertificateInvalidError{Reason: x509.CANotAuthorizedForThisName}))
	require.True(t, util.IsTLSExpiredError(x509.CertificateInvalidError{Reason: x509.Expired}))

	_, _, err = util.LoadTLSCertificates("", "wrong key", "wrong cert", true, 528)
	require.Error(t, err)
	_, _, err = util.LoadTLSCertificates("wrong ca", "/tmp/server-key.pem", "/tmp/server-cert.pem", true, 528)
	require.Error(t, err)
}

func TestErrorNoRollback(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	// Generate valid TLS certificates.
	caCert, caKey, err := generateCert(0, "TiDB CA", nil, nil, "/tmp/ca-key-rollback.pem", "/tmp/ca-cert-rollback.pem")
	require.NoError(t, err)
	_, _, err = generateCert(1, "tidb-server", caCert, caKey, "/tmp/server-key-rollback.pem", "/tmp/server-cert-rollback.pem")
	require.NoError(t, err)
	_, _, err = generateCert(2, "SQL Client Certificate", caCert, caKey, "/tmp/client-key-rollback.pem", "/tmp/client-cert-rollback.pem")
	require.NoError(t, err)
	err = registerTLSConfig("client-cert-rollback-test", "/tmp/ca-cert-rollback.pem", "/tmp/client-cert-rollback.pem", "/tmp/client-key-rollback.pem", "tidb-server", true)
	require.NoError(t, err)

	defer func() {
		os.Remove("/tmp/ca-key-rollback.pem")
		os.Remove("/tmp/ca-cert-rollback.pem")

		os.Remove("/tmp/server-key-rollback.pem")
		os.Remove("/tmp/server-cert-rollback.pem")
		os.Remove("/tmp/client-key-rollback.pem")
		os.Remove("/tmp/client-cert-rollback.pem")
	}()

	cli := newTestServerClient()
	cfg := newTestConfig()
	cfg.Port = cli.port
	cfg.Status.ReportStatus = false

	cfg.Security = config.Security{
		RequireSecureTransport: true,
		SSLCA:                  "wrong path",
		SSLCert:                "wrong path",
		SSLKey:                 "wrong path",
	}
	_, err = NewServer(cfg, ts.tidbdrv)
	require.Error(t, err)

	// test reload tls fail with/without "error no rollback option"
	cfg.Security = config.Security{
		SSLCA:   "/tmp/ca-cert-rollback.pem",
		SSLCert: "/tmp/server-cert-rollback.pem",
		SSLKey:  "/tmp/server-key-rollback.pem",
	}
	server, err := NewServer(cfg, ts.tidbdrv)
	require.NoError(t, err)
	cli.port = getPortFromTCPAddr(server.listener.Addr())
	go func() {
		err := server.Run()
		require.NoError(t, err)
	}()
	defer server.Close()
	time.Sleep(time.Millisecond * 100)
	connOverrider := func(config *mysql.Config) {
		config.TLSConfig = "client-cert-rollback-test"
	}
	err = cli.runTestTLSConnection(t, connOverrider)
	require.NoError(t, err)
	os.Remove("/tmp/server-key-rollback.pem")
	err = cli.runReloadTLS(t, connOverrider, false)
	require.Error(t, err)
	tlsCfg := server.getTLSConfig()
	require.NotNil(t, tlsCfg)
	err = cli.runReloadTLS(t, connOverrider, true)
	require.NoError(t, err)
	tlsCfg = server.getTLSConfig()
	require.Nil(t, tlsCfg)
}

func TestPrepareCount(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	qctx, err := ts.tidbdrv.OpenCtx(uint64(0), 0, uint8(tmysql.DefaultCollationID), "test", nil)
	require.NoError(t, err)
	prepareCnt := atomic.LoadInt64(&variable.PreparedStmtCount)
	ctx := context.Background()
	_, err = Execute(ctx, qctx, "use test;")
	require.NoError(t, err)
	_, err = Execute(ctx, qctx, "drop table if exists t1")
	require.NoError(t, err)
	_, err = Execute(ctx, qctx, "create table t1 (id int)")
	require.NoError(t, err)
	stmt, _, _, err := qctx.Prepare("insert into t1 values (?)")
	require.NoError(t, err)
	require.Equal(t, prepareCnt+1, atomic.LoadInt64(&variable.PreparedStmtCount))
	require.NoError(t, err)
	err = qctx.GetStatement(stmt.ID()).Close()
	require.NoError(t, err)
	require.Equal(t, prepareCnt, atomic.LoadInt64(&variable.PreparedStmtCount))
}

func TestDefaultCharacterAndCollation(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	// issue #21194
	collate.SetNewCollationEnabledForTest(true)
	defer collate.SetNewCollationEnabledForTest(false)
	// 255 is the collation id of mysql client 8 default collation_connection
	qctx, err := ts.tidbdrv.OpenCtx(uint64(0), 0, uint8(255), "test", nil)
	require.NoError(t, err)
	testCase := []struct {
		variable string
		except   string
	}{
		{"collation_connection", "utf8mb4_bin"},
		{"character_set_connection", "utf8mb4"},
		{"character_set_client", "utf8mb4"},
	}

	for _, tc := range testCase {
		sVars, b := qctx.GetSessionVars().GetSystemVar(tc.variable)
		require.True(t, b)
		require.Equal(t, tc.except, sVars)
	}
}

func TestReloadTLS(t *testing.T) {
	ts, cleanup := createTidbTestSuite(t)
	defer cleanup()

	// Generate valid TLS certificates.
	caCert, caKey, err := generateCert(0, "TiDB CA", nil, nil, "/tmp/ca-key-reload.pem", "/tmp/ca-cert-reload.pem")
	require.NoError(t, err)
	_, _, err = generateCert(1, "tidb-server", caCert, caKey, "/tmp/server-key-reload.pem", "/tmp/server-cert-reload.pem")
	require.NoError(t, err)
	_, _, err = generateCert(2, "SQL Client Certificate", caCert, caKey, "/tmp/client-key-reload.pem", "/tmp/client-cert-reload.pem")
	require.NoError(t, err)
	err = registerTLSConfig("client-certificate-reload", "/tmp/ca-cert-reload.pem", "/tmp/client-cert-reload.pem", "/tmp/client-key-reload.pem", "tidb-server", true)
	require.NoError(t, err)

	defer func() {
		os.Remove("/tmp/ca-key-reload.pem")
		os.Remove("/tmp/ca-cert-reload.pem")

		os.Remove("/tmp/server-key-reload.pem")
		os.Remove("/tmp/server-cert-reload.pem")
		os.Remove("/tmp/client-key-reload.pem")
		os.Remove("/tmp/client-cert-reload.pem")
	}()

	// try old cert used in startup configuration.
	cli := newTestServerClient()
	cfg := newTestConfig()
	cfg.Port = cli.port
	cfg.Status.ReportStatus = false
	cfg.Security = config.Security{
		SSLCA:   "/tmp/ca-cert-reload.pem",
		SSLCert: "/tmp/server-cert-reload.pem",
		SSLKey:  "/tmp/server-key-reload.pem",
	}
	server, err := NewServer(cfg, ts.tidbdrv)
	require.NoError(t, err)
	cli.port = getPortFromTCPAddr(server.listener.Addr())
	go func() {
		err := server.Run()
		require.NoError(t, err)
	}()
	time.Sleep(time.Millisecond * 100)
	// The client provides a valid certificate.
	connOverrider := func(config *mysql.Config) {
		config.TLSConfig = "client-certificate-reload"
	}
	err = cli.runTestTLSConnection(t, connOverrider)
	require.NoError(t, err)

	// try reload a valid cert.
	tlsCfg := server.getTLSConfig()
	cert, err := x509.ParseCertificate(tlsCfg.Certificates[0].Certificate[0])
	require.NoError(t, err)
	oldExpireTime := cert.NotAfter
	_, _, err = generateCert(1, "tidb-server", caCert, caKey, "/tmp/server-key-reload2.pem", "/tmp/server-cert-reload2.pem", func(c *x509.Certificate) {
		c.NotBefore = time.Now().Add(-24 * time.Hour).UTC()
		c.NotAfter = time.Now().Add(1 * time.Hour).UTC()
	})
	require.NoError(t, err)
	err = os.Rename("/tmp/server-key-reload2.pem", "/tmp/server-key-reload.pem")
	require.NoError(t, err)
	err = os.Rename("/tmp/server-cert-reload2.pem", "/tmp/server-cert-reload.pem")
	require.NoError(t, err)
	connOverrider = func(config *mysql.Config) {
		config.TLSConfig = "skip-verify"
	}
	err = cli.runReloadTLS(t, connOverrider, false)
	require.NoError(t, err)
	connOverrider = func(config *mysql.Config) {
		config.TLSConfig = "client-certificate-reload"
	}
	err = cli.runTestTLSConnection(t, connOverrider)
	require.NoError(t, err)

	tlsCfg = server.getTLSConfig()
	cert, err = x509.ParseCertificate(tlsCfg.Certificates[0].Certificate[0])
	require.NoError(t, err)
	newExpireTime := cert.NotAfter
	require.True(t, newExpireTime.After(oldExpireTime))

	// try reload a expired cert.
	_, _, err = generateCert(1, "tidb-server", caCert, caKey, "/tmp/server-key-reload3.pem", "/tmp/server-cert-reload3.pem", func(c *x509.Certificate) {
		c.NotBefore = time.Now().Add(-24 * time.Hour).UTC()
		c.NotAfter = c.NotBefore.Add(1 * time.Hour).UTC()
	})
	require.NoError(t, err)
	err = os.Rename("/tmp/server-key-reload3.pem", "/tmp/server-key-reload.pem")
	require.NoError(t, err)
	err = os.Rename("/tmp/server-cert-reload3.pem", "/tmp/server-cert-reload.pem")
	require.NoError(t, err)
	connOverrider = func(config *mysql.Config) {
		config.TLSConfig = "skip-verify"
	}
	err = cli.runReloadTLS(t, connOverrider, false)
	require.NoError(t, err)
	connOverrider = func(config *mysql.Config) {
		config.TLSConfig = "client-certificate-reload"
	}
	err = cli.runTestTLSConnection(t, connOverrider)
	require.NotNil(t, err)
	require.Truef(t, util.IsTLSExpiredError(err), "real error is %+v", err)
	server.Close()
}
