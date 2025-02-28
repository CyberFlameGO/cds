package hatchery

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/pkg/errors"
	"github.com/rockbears/log"

	"github.com/ovh/cds/sdk"
)

// workerRegister is called by a ticker.
// the hatchery checks each worker model, and if a worker model needs to
// be registered, the hatchery calls SpawnWorker().
// each ticker can trigger 5 worker models (maximum)
// and 5 worker models can be spawned in same time, in the case of a spawn takes longer
// than a tick.
var nbRegisteringWorkerModels int64

func workerRegister(ctx context.Context, h InterfaceWithModels, startWorkerChan chan<- workerStarterRequest) error {
	if len(models) == 0 {
		return errors.Errorf("no model returned by GetWorkerModels")
	}
	// currentRegister contains the register spawned in this ticker
	currentRegistering, err := WorkerPool(ctx, h, sdk.StatusWorkerRegistering)
	if err != nil {
		log.Error(ctx, "worker pool error: %v", err)
	}

	atomic.StoreInt64(&nbRegisteringWorkerModels, int64(len(currentRegistering)))
loopModels:
	for k := range models {
		if models[k].Type != h.ModelType() {
			continue
		}
		if h.CanSpawn(ctx, &models[k], 0, nil) && (h.NeedRegistration(ctx, &models[k]) || models[k].CheckRegistration) {
			log.Debug(ctx, "model %q need to register", models[k].Path())
		} else {
			continue
		}

		maxRegistration := int64(h.Configuration().Provision.MaxConcurrentRegistering)
		if maxRegistration == 0 {
			maxRegistration = 2
		}
		if atomic.LoadInt64(&nbRegisteringWorkerModels) > maxRegistration {
			log.Debug(ctx, "max registering worker reached")
			return nil
		}

		if !checkCapacities(ctx, h) {
			log.Debug(ctx, "unable to register now")
			return nil
		}

		// Check if there is a pending registering worker
		for _, w := range currentRegistering {
			if strings.Contains(w.Name, models[k].Name) {
				log.Info(ctx, "model %q is already registering (%s)", models[k].Name, w.Name)
				continue loopModels
			}
		}

		// if current hatchery is in same group than worker model -> do not avoid spawn, even if worker model is in error
		if models[k].NbSpawnErr > 5 {
			log.Warn(ctx, "Too many errors on spawn with model %s, please check this worker model", models[k].Name)
			continue
		}

		if err := h.CDSClient().WorkerModelBook(models[k].Group.Name, models[k].Name); err != nil {
			ctx := log.ContextWithStackTrace(ctx, err)
			log.Error(ctx, "cannot book model %s with id %d: %v", models[k].Path(), models[k].ID, err)
			continue
		}

		log.Info(ctx, "model %q (%d) has been booked and will be spawned for registration", models[k].Name, models[k].ID)

		// Interpolate model secrets
		if err := ModelInterpolateSecrets(h, &models[k]); err != nil {
			ctx := log.ContextWithStackTrace(ctx, err)
			log.Error(ctx, "cannot interpolate secrets for model %s: %v", models[k].Path(), err)
			continue
		}

		//Ask for the creation
		startWorkerChan <- workerStarterRequest{
			registerWorkerModel: &models[k],
		}
	}
	return nil
}

// CheckWorkerModelRegister checks if a model has been registered, if not it raises an error on the API
func CheckWorkerModelRegister(ctx context.Context, h Interface, modelPath string) error {
	var sendError bool
	for i := range models {
		if models[i].Group.Name+"/"+models[i].Name == modelPath {
			sendError = models[i].NeedRegistration
			break
		}
	}
	if !sendError {
		// need registration is false, no error to return
		return nil
	}

	// it's need registration = true ->
	// perhaps that the models list is not up to date
	// so, we call a fresh model list to re-check the flag need registration known by the api
	hWithModels, isWithModels := h.(InterfaceWithModels)
	if isWithModels {
		modelsFresh, err := hWithModels.WorkerModelsEnabled()
		if err != nil {
			ctx := log.ContextWithStackTrace(ctx, err)
			log.Error(ctx, "error on h.CheckWorkerModelRegister(): %v", err)
			return err
		}

		for i := range modelsFresh {
			if modelsFresh[i].Group.Name+"/"+modelsFresh[i].Name == modelPath {
				sendError = modelsFresh[i].NeedRegistration
				break
			}
		}
	}

	if sendError {
		// need registration stay to true, even after a fresh call to api -> error
		return sdk.WithStack(sdk.ErrWorkerModelDeploymentFailed)
	}
	return nil
}
