//go:build integration

package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/oxyno-zeta/s3-proxy/pkg/s3-proxy/config"
	cmocks "github.com/oxyno-zeta/s3-proxy/pkg/s3-proxy/config/mocks"
	"github.com/oxyno-zeta/s3-proxy/pkg/s3-proxy/log"
	"github.com/oxyno-zeta/s3-proxy/pkg/s3-proxy/s3client"
	"github.com/oxyno-zeta/s3-proxy/pkg/s3-proxy/tracing"
	"github.com/oxyno-zeta/s3-proxy/pkg/s3-proxy/webhook"
)

func TestPutMultipartFormTemporaryFilesCleanup(t *testing.T) {
	accessKey := "YOUR-ACCESSKEYID"
	secretAccessKey := "YOUR-SECRETACCESSKEY"
	region := "eu-central-1"
	bucket := "test-bucket"

	_, s3server, err := setupFakeS3(
		accessKey,
		secretAccessKey,
		region,
		bucket,
	)
	require.NoError(t, err)

	defer s3server.Close()

	cfg := &config.Config{
		Server: &config.ServerConfig{
			Compress: &config.ServerCompressConfig{
				Enabled: &config.DefaultServerCompressEnabled,
				Level:   config.DefaultServerCompressLevel,
				Types:   config.DefaultServerCompressTypes,
			},
		},
		ListTargets: &config.ListTargetsConfig{},
		Tracing:     &config.TracingConfig{},
		Templates:   testsDefaultGeneralTemplateConfig,
		Targets: map[string]*config.TargetConfig{
			"target1": {
				Name: "target1",
				Bucket: &config.BucketConfig{
					Name:       bucket,
					Region:     region,
					S3Endpoint: s3server.URL,
					Credentials: &config.BucketCredentialConfig{
						AccessKey: &config.CredentialConfig{Value: accessKey},
						SecretKey: &config.CredentialConfig{Value: secretAccessKey},
					},
					DisableSSL: true,
				},
				Mount: &config.MountConfig{
					Path: []string{"/mount/"},
				},
				Actions: &config.ActionsConfig{
					PUT: &config.PutActionConfig{
						Enabled: true,
					},
				},
			},
		},
	}

	tests := []struct {
		name         string
		inputFileKey string
		expectedCode int
	}{
		{
			name:         "PUT with the expected form field name",
			inputFileKey: "file",
			expectedCode: 204,
		},
		{
			name:         "PUT with a wrong form field name",
			inputFileKey: "wrongkey",
			expectedCode: 500,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a dedicated temporary directory to catch multipart temporary files
			tmpDir := t.TempDir()
			t.Setenv("TMPDIR", tmpDir)

			// Create go mock controller
			ctrl := gomock.NewController(t)
			cfgManagerMock := cmocks.NewMockManager(ctrl)

			// Load configuration in manager
			cfgManagerMock.EXPECT().GetConfig().AnyTimes().Return(cfg)

			logger := log.NewLogger()
			// Create tracing service
			tsvc, err := tracing.New(cfgManagerMock, logger)
			require.NoError(t, err)

			// Create S3 Manager
			s3Manager := s3client.NewManager(cfgManagerMock, metricsCtx)
			err = s3Manager.Load()
			require.NoError(t, err)

			svr := &Server{
				logger:          logger,
				cfgManager:      cfgManagerMock,
				metricsCl:       metricsCtx,
				tracingSvc:      tsvc,
				s3clientManager: s3Manager,
				webhookManager:  webhook.NewManager(cfgManagerMock, metricsCtx),
			}
			got, err := svr.generateRouter()
			require.NoError(t, err)

			// Build multipart form
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)
			part, err := writer.CreateFormFile(tt.inputFileKey, "cleanup.txt")
			require.NoError(t, err)
			_, err = part.Write([]byte("Hello cleanup!"))
			require.NoError(t, err)
			require.NoError(t, writer.Close())

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, "http://localhost/mount/folder1/", body)
			require.NoError(t, err)
			req.Header.Set("Content-Type", writer.FormDataContentType())

			w := httptest.NewRecorder()

			got.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			// Check that no multipart temporary file is left behind
			entries, err := os.ReadDir(tmpDir)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}
