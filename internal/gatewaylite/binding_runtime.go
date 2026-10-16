package gatewaylite

import (
	"context"
	"database/sql"
	"time"

	"github.com/redis/go-redis/v9"
)

func (m *BindingManager) ConfigureDrain(db *sql.DB, rdb *redis.Client) {
	m.mediaDB = db
	m.drain = BindingDrainProbe(db, rdb)
}

// StartActivation uses the same immutable destination as this runtime. It does
// not swap the shared client; only a clean process generation can activate it.
func (m *BindingManager) StartActivation(syncFull func(context.Context) error) {
	destination:=m.RuntimeDestination()
	if destination==nil{return}
	generationID:=destination.BindingID
	bindingActivationWorkers.Go(context.Background(), func(ctx context.Context) {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			r := m.Record()
			d:=m.RuntimeDestination()
			if d==nil || d.BindingID!=generationID{return}
			if r.Current != nil && r.DisconnectOperation != "" && (r.Phase == "draining" || r.Phase == "blocked") {
				attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
				_, _ = m.Disconnect(attempt)
				cancel()
			} else if r.Phase == "activating" {
				attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
				_ = m.activateGeneration(attempt, generationID, syncFull)
				cancel()
			} else if r.Current!=nil {
				// Keep a healthy generation's watcher available for later retry/cancel
				// transitions. It performs no network or database work while bound.
			} else {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

var bindingActivationWorkers workerLifetime

func ResetBindingActivationWorkers() { bindingActivationWorkers = workerLifetime{} }
