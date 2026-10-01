// Package handler — media delete endpoint.
package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

func deleteMediaHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		deleteFiles := parseDeleteFilesQuery(c.Query("delete_files"))
		if err := svc.Media.DeleteMedia(c.Request.Context(), c.Param("id"), deleteFiles); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func parseDeleteFilesQuery(raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return false
	}
	if raw == "1" || raw == "true" || raw == "yes" {
		return true
	}
	v, err := strconv.ParseBool(raw)
	return err == nil && v
}
