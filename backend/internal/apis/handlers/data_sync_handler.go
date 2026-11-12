package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/services"

	"github.com/gin-gonic/gin"
)

type DataSyncHandler struct{ service *services.DataSyncService }

func NewDataSyncHandler(service *services.DataSyncService) *DataSyncHandler {
	return &DataSyncHandler{service: service}
}

func dataSyncIdentity(c *gin.Context) (string, string, bool) {
	owner := c.GetString("userID")
	if owner == "" {
		message := "请先登录"
		c.JSON(401, dtos.Response{Success: false, Error: &message})
		return "", "", false
	}
	return owner, c.Param("id"), true
}
func dataSyncWriteError(c *gin.Context, err error) {
	failure := &services.DataSyncError{Status: 503, Code: "SYNC_STATE_UNAVAILABLE"}
	var typed *services.DataSyncError
	if errors.As(err, &typed) {
		failure = typed
	}
	message := services.SyncErrorMessage(failure.Code)
	c.JSON(failure.Status, dtos.Response{Success: false, Error: &message, Data: gin.H{"code": failure.Code, "source_id": failure.SourceID, "job_ref": failure.JobRef, "block_reason": failure.Code}})
}
func (h *DataSyncHandler) Status(c *gin.Context) {
	owner, chat, ok := dataSyncIdentity(c)
	if !ok {
		return
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		dataSyncWriteError(c, &services.DataSyncError{Status: 400, Code: "INVALID_REQUEST"})
		return
	}
	request := ""
	for key, values := range query {
		if key != "request_id" || len(values) != 1 || !models.CanonicalSyncUUID(values[0]) {
			dataSyncWriteError(c, &services.DataSyncError{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
		request = values[0]
	}
	status, err := h.service.Status(c.Request.Context(), owner, chat, request)
	if err != nil {
		dataSyncWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, dtos.Response{Success: true, Data: status})
}
func (h *DataSyncHandler) GetJob(c *gin.Context) {
	owner, chat, ok := dataSyncIdentity(c)
	if !ok {
		return
	}
	job, err := h.service.GetJob(c.Request.Context(), owner, chat, c.Param("jobID"))
	if err != nil {
		dataSyncWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, dtos.Response{Success: true, Data: job})
}
func decodeDataSyncRequest(reader io.Reader) (string, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 513))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", errors.New("INVALID_REQUEST")
	}
	if !decoder.More() {
		return "", errors.New("INVALID_REQUEST")
	}
	key, err := decoder.Token()
	if err != nil || key != "request_id" {
		return "", errors.New("INVALID_REQUEST")
	}
	var id string
	if decoder.Decode(&id) != nil || !models.CanonicalSyncUUID(id) || decoder.More() {
		return "", errors.New("INVALID_REQUEST")
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return "", errors.New("INVALID_REQUEST")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return "", errors.New("INVALID_REQUEST")
	}
	return id, nil
}
func (h *DataSyncHandler) CreateJob(c *gin.Context) {
	owner, chat, ok := dataSyncIdentity(c)
	if !ok {
		return
	}
	request, err := decodeDataSyncRequest(http.MaxBytesReader(c.Writer, c.Request.Body, 512))
	if err != nil {
		dataSyncWriteError(c, &services.DataSyncError{Status: 400, Code: "INVALID_REQUEST"})
		return
	}
	job, err := h.service.CreateJob(c.Request.Context(), owner, chat, request)
	if err != nil {
		dataSyncWriteError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, dtos.Response{Success: true, Data: job})
}
