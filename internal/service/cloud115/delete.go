// 115 开放平台删除类 API：元数据覆盖上传前清理远端旧文件。
package cloud115

import (
	"context"
	"strings"
)

// DeleteFiles 批量删除 115 文件（官方接口 POST /open/ufile/delete）。
// 删除为异步执行，文件移入回收站。parentID 为待删除文件所在父目录 ID
//（可选提示，空串省略）。fileIDs 中的空项自动忽略，全为空时直接返回成功。
func (c *OpenClient) DeleteFiles(ctx context.Context, parentID string, fileIDs ...string) error {
	ids := make([]string, 0, len(fileIDs))
	for _, id := range fileIDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	params := map[string]string{"file_ids": strings.Join(ids, ",")}
	if parentID = strings.TrimSpace(parentID); parentID != "" {
		params["parent_id"] = parentID
	}
	resp, err := c.doAuthJSON(ctx, "POST", ProAPIBase+"/open/ufile/delete", params, 2)
	if err != nil {
		return err
	}
	// doJSON 已把 state=false 转为错误返回，这里兜底防御响应外壳异常
	if !resp.State {
		return NewOpenAPIResponseError(resp.Code, resp.Errno, resp.Message, resp.Error, "115 删除文件失败")
	}
	return nil
}
