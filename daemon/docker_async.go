// docker_async.go — Helpers para handlers Docker con soporte sync/async (Beta 8.1.x).
//
// APP-014 · dockerInstall async vía operationsRepo
// APP-053 · dockerPull async vía operationsRepo
//
// Patrón:
//
//   1. Handler valida auth + body (síncrono, rápido).
//   2. Si query string contiene async=true:
//      - operationsRepo.Create(...) crea row con status='pending'
//      - go func() { worker(...) } ejecuta el trabajo en background
//      - response 202 Accepted con {operationId, pollUrl}
//   3. Si no:
//      - worker(...) se ejecuta inline
//      - response 200 OK con resultado (legacy)
//
// El worker es una función pura `(ctx, params...) (result, error)` que NO
// conoce HTTP. Solo conoce trabajo real. Se reutiliza idéntica para sync y
// async; el wrapper handler decide cómo se responde.
//
// Para preservar la semántica HTTP de los errores específicos (e.g. 409 de
// APP-063 en dockerInstall), los workers devuelven `*httpStatusError` cuando
// el código importa. El wrapper sync lo mapea a status HTTP; el wrapper
// async lo aplana a string para el campo error de la operation.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
)

// httpStatusError · error tipado con código HTTP específico.
// Cuando un worker devuelve este tipo, el handler sync usa Code en lugar de
// 500 por defecto. El handler async ignora Code y guarda solo Msg.
//
// NO se exporta · solo se usa internamente en handlers que se refactorizan
// a worker. No es contrato HTTP del paquete.
type httpStatusError struct {
	Code int
	Msg  string
}

func (e *httpStatusError) Error() string { return e.Msg }

// asHTTPError envuelve un error con un código HTTP específico.
// Helper para mantener la semántica del jsonError original al refactorizar.
func asHTTPError(code int, format string, args ...interface{}) error {
	return &httpStatusError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// isAsyncRequested devuelve true si la query string del request contiene
// async=true (o "1", o "yes"). Permite alguna flexibilidad sin ser laxo.
func isAsyncRequested(r *http.Request) bool {
	v := r.URL.Query().Get("async")
	return v == "true" || v == "1" || v == "yes"
}

// writeWorkerError envía la respuesta HTTP correcta para un error del worker.
// Si es httpStatusError, usa su código; si no, 500 genérico.
func writeWorkerError(w http.ResponseWriter, err error) {
	if hse, ok := err.(*httpStatusError); ok {
		jsonError(w, hse.Code, hse.Msg)
		return
	}
	jsonError(w, 500, err.Error())
}

// writeAsyncAccepted envía un 202 con el payload estándar de "operation
// pendiente". Usado por handlers que aceptan ?async=true.
func writeAsyncAccepted(w http.ResponseWriter, op *DBOperation) {
	jsonResponse(w, http.StatusAccepted, map[string]interface{}{
		"operationId": op.ID,
		"pollUrl":     "/api/operations/" + op.ID,
		"status":      op.Status,
		"type":        op.Type,
	})
}

// runWorkerAsync envuelve la ejecución de un worker en una goroutine que
// reporta el resultado a operationsRepo. Centraliza el patrón:
//
//   1. MarkRunning
//   2. invocar worker
//   3. MarkSucceeded(resultJSON) ó MarkFailed(error)
//
// El worker recibe ctx (context.Background, dado que el request HTTP ya
// terminó) y devuelve (result map, error). Si result no es nil al success,
// se serializa a JSON para el campo result_json de la operation.
//
// runWorkerAsync NO bloquea · lanza la goroutine y retorna.
func runWorkerAsync(opID string, work func(ctx context.Context) (map[string]interface{}, error)) {
	go func() {
		ctx := context.Background()

		if err := operationsRepo.MarkRunning(ctx, opID); err != nil {
			logMsg("docker_async: MarkRunning failed for %s: %v", opID, err)
			// Continuamos: el trabajo puede que aún tenga sentido aunque
			// el state machine se haya descarriado. Marcamos como failed
			// abajo si peta. Si MarkRunning falló por race con cancel,
			// el siguiente Mark también fallará y al menos quedará trazado.
		}

		result, err := work(ctx)
		if err != nil {
			if markErr := operationsRepo.MarkFailed(ctx, opID, err.Error(), ""); markErr != nil {
				logMsg("docker_async: MarkFailed failed for %s: %v (orig err: %v)", opID, markErr, err)
			}
			return
		}

		resultJSON := ""
		if result != nil {
			if data, jerr := json.Marshal(result); jerr == nil {
				resultJSON = string(data)
			}
		}
		if markErr := operationsRepo.MarkSucceeded(ctx, opID, resultJSON); markErr != nil {
			logMsg("docker_async: MarkSucceeded failed for %s: %v", opID, markErr)
		}
	}()
}

// updateOpProgressSafe · helper para workers que quieren reportar progreso
// solo si están corriendo bajo una operation (modo async).
//
// Si opID es vacío o el repo no está disponible, es no-op silencioso.
// Esto permite escribir workers sin if-else: pasan el opID y el helper
// decide. En sync (opID=""), no hace nada.
func updateOpProgressSafe(ctx context.Context, opID string, progress int, message string) {
	if opID == "" || operationsRepo == nil {
		return
	}
	// Errores se loguean pero no propagan · el progreso es metadata, su
	// fallo no debe abortar el trabajo real.
	if err := operationsRepo.UpdateProgress(ctx, opID, progress, message); err != nil {
		logMsg("docker_async: UpdateProgress failed for %s: %v", opID, err)
	}
}

// getStackHostIP devuelve la primera IP IPv4 no-loopback del host, usada
// como valor por defecto para HOST_IP en los .env de los stacks Docker.
//
// Apps del catálogo NimOS (p.ej. Jellyfin con JELLYFIN_PublishedServerUrl)
// necesitan saber la IP del NAS para generar URLs absolutas dentro del LAN.
// El backend es la fuente canónica · el frontend no debería adivinar.
//
// Fallback: si no se encuentra ninguna IP IPv4 válida, devuelve "127.0.0.1"
// (mejor que cadena vacía · al menos el container arranca).
func getStackHostIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "127.0.0.1"
}
