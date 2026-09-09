package ingest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	getoffyoursaasiov1alpha1 "github.com/circa10a/getoffyoursaas/api/v1alpha1"
)

// testName is the StepScaler (and target Deployment) name used by these tests.
const testName = "app"

// testServer builds a Server backed by a fake client seeded with one
// StepScaler. An optional interceptor.Funcs lets a test inject a failure
// from the underlying client without changing production code.
func testServer(t *testing.T, funcs ...interceptor.Funcs) (*Server, func() *getoffyoursaasiov1alpha1.StepScaler) {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := getoffyoursaasiov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}

	ss := &getoffyoursaasiov1alpha1.StepScaler{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: "default"},
		Spec: getoffyoursaasiov1alpha1.StepScalerSpec{
			ScaleTargetRef: autoscalingv1.CrossVersionObjectReference{
				APIVersion: "apps/v1", Kind: "Deployment", Name: testName,
			},
			ContainerName: "*",
			DailyStepGoal: 10000,
			MinCPU:        resource.MustParse("100m"),
			MaxCPU:        resource.MustParse("1000m"),
		},
	}

	builder := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ss).
		WithStatusSubresource(ss)
	if len(funcs) > 0 {
		builder = builder.WithInterceptorFuncs(funcs[0])
	}
	c := builder.Build()

	get := func() *getoffyoursaasiov1alpha1.StepScaler {
		out := &getoffyoursaasiov1alpha1.StepScaler{}
		if err := c.Get(context.Background(),
			types.NamespacedName{Name: testName, Namespace: "default"}, out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	return &Server{Client: c}, get
}

func post(srv *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/steps", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAcceptsValidReading(t *testing.T) {
	srv, get := testServer(t)

	rec := post(srv, `{"steps": 7431}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	ss := get()
	if ss.Status.Steps == nil || *ss.Status.Steps != 7431 {
		t.Errorf("status.steps = %v, want 7431", ss.Status.Steps)
	}
	if ss.Status.StepsUpdatedAt == nil {
		t.Error("status.stepsUpdatedAt was not set")
	}
}

func TestRejectsBadReadings(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"negative steps", `{"steps": -1}`},
		{"absurdly high steps", `{"steps": 200001}`},
		{"not json", `nope`},
		{"steps is a string", `{"steps": "7431"}`},
		{"steps missing", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, get := testServer(t)
			rec := post(srv, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("got status %d, want 400", rec.Code)
			}
			if ss := get(); ss.Status.Steps != nil {
				t.Errorf("status.steps was written despite a bad request: %v", *ss.Status.Steps)
			}
		})
	}
}

func TestRejectsNonPost(t *testing.T) {
	srv, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/steps", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("got status %d, want 405", rec.Code)
	}
}

func TestZeroStepsIsValid(t *testing.T) {
	srv, get := testServer(t)
	if rec := post(srv, `{"steps": 0}`); rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want 204", rec.Code)
	}
	if ss := get(); ss.Status.Steps == nil || *ss.Status.Steps != 0 {
		t.Errorf("status.steps = %v, want 0", ss.Status.Steps)
	}
}

func TestAcceptsUpperBound(t *testing.T) {
	srv, get := testServer(t)
	rec := post(srv, `{"steps": 200000}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if ss := get(); ss.Status.Steps == nil || *ss.Status.Steps != 200000 {
		t.Errorf("status.steps = %v, want 200000", ss.Status.Steps)
	}
}

func TestHonoursAsOf(t *testing.T) {
	srv, get := testServer(t)
	asOf := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	body := fmt.Sprintf(`{"steps": 500, "asOf": %q}`, asOf.Format(time.RFC3339))

	rec := post(srv, body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	ss := get()
	if ss.Status.StepsUpdatedAt == nil {
		t.Fatal("status.stepsUpdatedAt was not set")
	}
	if !ss.Status.StepsUpdatedAt.Time.Equal(asOf) {
		t.Errorf("status.stepsUpdatedAt = %v, want %v", ss.Status.StepsUpdatedAt.Time, asOf)
	}
}

func TestListFailureReturns500(t *testing.T) {
	wantErr := errors.New("boom")
	srv, get := testServer(t, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			return wantErr
		},
	})

	rec := post(srv, `{"steps": 100}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("got status %d, want 500", rec.Code)
	}
	if ss := get(); ss.Status.Steps != nil {
		t.Errorf("status.steps was written despite a List failure: %v", *ss.Status.Steps)
	}
}
