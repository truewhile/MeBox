package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/truewhile/MeBox/internal/service"
)

func backgroundTasksHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || svc.Tasks == nil {
			c.JSON(http.StatusOK, service.TaskSnapshot{Active: []service.BackgroundTask{}, Recent: []service.BackgroundTask{}})
			return
		}
		c.JSON(http.StatusOK, svc.Tasks.Snapshot())
	}
}
