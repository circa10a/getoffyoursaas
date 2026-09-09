// Package ingest receives step-count readings from the iPhone Shortcut and
// records them on StepScaler status.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	getoffyoursaasiov1alpha1 "github.com/circa10a/getoffyoursaas/api/v1alpha1"
)

// MaxSteps bounds accepted readings. The world record for a day is well under
// this; the bound exists so a garbage payload cannot mint an unschedulable
// CPU request.
const MaxSteps int64 = 200000

type Server struct {
	Client client.Client
	Addr   string
}

// NeedLeaderElection opts this server out of the leader-election group.
//
// Without it, controller-runtime treats a plain Runnable as leader-gated
// (runnable_group.go sends anything not implementing LeaderElectionRunnable to
// the leader-election group) and the deployment passes --leader-elect, so the
// listener would only start after the lease is won. That breaks readiness:
// the probe is healthz.Ping, which reports Ready immediately, so the Service
// would route requests to a pod that is not listening yet. Serving from every
// replica is also simply correct here — writes are conflict-safe via
// RetryOnConflict, so there is nothing to serialise behind a leader.
func (s *Server) NeedLeaderElection() bool { return false }

type reading struct {
	Steps *int64     `json:"steps"`
	AsOf  *time.Time `json:"asOf,omitempty"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/steps", s.handleSteps)
	return mux
}

// Start implements manager.Runnable.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	logf.FromContext(ctx).Info("ingest server listening", "addr", s.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handleSteps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST is allowed", http.StatusMethodNotAllowed)
		return
	}

	var in reading
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		http.Error(w, "malformed JSON body", http.StatusBadRequest)
		return
	}
	if in.Steps == nil {
		http.Error(w, `field "steps" is required`, http.StatusBadRequest)
		return
	}
	if *in.Steps < 0 || *in.Steps > MaxSteps {
		http.Error(w, "steps must be between 0 and 200000", http.StatusBadRequest)
		return
	}

	asOf := metav1.Now()
	if in.AsOf != nil {
		asOf = metav1.NewTime(*in.AsOf)
	}

	ctx := r.Context()
	var list getoffyoursaasiov1alpha1.StepScalerList
	if err := s.Client.List(ctx, &list); err != nil {
		http.Error(w, "could not list StepScalers", http.StatusInternalServerError)
		return
	}

	for i := range list.Items {
		key := client.ObjectKeyFromObject(&list.Items[i])
		// Two writers touch status (this handler and the reconciler), so
		// conflicts are routine rather than exceptional.
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var ss getoffyoursaasiov1alpha1.StepScaler
			if err := s.Client.Get(ctx, key, &ss); err != nil {
				return err
			}
			steps := *in.Steps
			ss.Status.Steps = &steps
			ss.Status.StepsUpdatedAt = &asOf
			return s.Client.Status().Update(ctx, &ss)
		})
		if err != nil {
			logf.FromContext(ctx).Error(err, "could not record steps", "stepscaler", key)
			http.Error(w, "could not record steps", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
