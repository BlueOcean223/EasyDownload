package downloader

import (
	"encoding/json"
	"os"
)

// multipartState 表示多线程下载的持久化状态，用于断点续传。
type multipartState struct {
	Version   int                   `json:"version"`   // 状态文件版本号
	TotalSize int64                 `json:"totalSize"` // 文件总大小
	Threads   int                   `json:"threads"`   // 下载线程数
	Chunks    []multipartStateChunk `json:"chunks"`    // 各分片状态
}

// multipartStateChunk 表示单个下载分片的状态。
type multipartStateChunk struct {
	Index      int   `json:"index"`      // 分片索引
	Start      int64 `json:"start"`      // 分片起始字节位置
	End        int64 `json:"end"`        // 分片结束字节位置
	Downloaded int64 `json:"downloaded"` // 已下载字节数
	Done       bool  `json:"done"`       // 是否已完成
}

// multipartStatePath 返回状态文件的路径（原文件路径 + .edstate.json）。
func multipartStatePath(outputPath string) string {
	return outputPath + ".edstate.json"
}

// multipartStateExists 检查状态文件是否存在。
func multipartStateExists(outputPath string) bool {
	_, err := os.Stat(multipartStatePath(outputPath))
	return err == nil
}

// MultipartStateExists 检查指定输出文件的多线程下载状态是否存在。
func MultipartStateExists(outputPath string) bool {
	return multipartStateExists(outputPath)
}

// loadMultipartState 从磁盘加载多线程下载状态。
func loadMultipartState(outputPath string) (*multipartState, error) {
	b, err := os.ReadFile(multipartStatePath(outputPath))
	if err != nil {
		return nil, err
	}
	var st multipartState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// LoadMultipartTotalSize 获取多线程下载状态中记录的文件总大小。
func LoadMultipartTotalSize(outputPath string) (int64, error) {
	st, err := loadMultipartState(outputPath)
	if err != nil {
		return 0, err
	}
	return st.TotalSize, nil
}

// saveMultipartState 将多线程下载状态保存到磁盘。
// 使用临时文件 + 重命名的方式确保原子写入。
func saveMultipartState(outputPath string, st *multipartState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := multipartStatePath(outputPath) + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	_ = os.Remove(multipartStatePath(outputPath))
	return os.Rename(tmp, multipartStatePath(outputPath))
}
