// 调度器：云下载任务轮询。
//
// 单独成文件而不是塞进 scheduler_local_jobs.go：那个文件是本地媒体相关的
// 周期任务，云下载是外部网盘任务，两者的失败处理与限流策略不同。
package service

import "context"

// jobCloudDownloadPoll 轮询 115 云下载任务状态。
// 没有活跃任务时 PollOnce 直接返回，不发任何 115 请求。
func (s *SchedulerService) jobCloudDownloadPoll(ctx context.Context) error {
	if s.cloudDownload == nil {
		return nil
	}
	return s.cloudDownload.PollOnce(ctx)
}
