// Package utils 提供通用工具函数
package utils

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ZipFile 表示要添加到 ZIP 压缩包中的单个文件
type ZipFile struct {
	Path string // 源文件的本地路径
	Name string // 在 ZIP 压缩包中的文件名
}

// WriteZipOptions 定义原子写入 ZIP 文件时的可选配置
type WriteZipOptions struct {
	// BufferSize 指定写入缓冲区大小，大于 0 时启用缓冲写入
	BufferSize int
	// Overwrite 若为 true，在重命名临时文件前先删除目标文件
	// 这在 Windows 上很有用，因为 os.Rename 在目标文件已存在时会失败
	Overwrite bool
}

// WriteZipAtomic 以原子方式创建 ZIP 压缩包
//
// 原子性保证：先写入临时文件，成功后再重命名为目标文件，
// 避免写入过程中出错导致目标文件损坏或不完整。
//
// 参数:
//   - destPath: 目标 ZIP 文件路径
//   - files: 要压缩的文件列表
//   - opts: 写入选项
//
// 返回:
//   - error: 创建失败时返回错误，成功返回 nil
func WriteZipAtomic(destPath string, files []ZipFile, opts WriteZipOptions) (err error) {
	// 参数校验
	if destPath == "" {
		return fmt.Errorf("empty dest path")
	}

	// 确保目标目录存在
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	// 创建临时文件，避免直接写入目标文件
	tmpPath := destPath + ".tmp"
	outFile, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	// 清理逻辑：确保文件关闭，出错时删除临时文件
	closed := false
	defer func() {
		if !closed {
			_ = outFile.Close()
		}
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	// 根据配置决定是否使用缓冲写入
	var writer io.Writer = outFile
	var bw *bufio.Writer
	if opts.BufferSize > 0 {
		bw = bufio.NewWriterSize(outFile, opts.BufferSize)
		writer = bw
	}

	// 遍历文件列表，逐个写入 ZIP
	zw := zip.NewWriter(writer)
	for _, f := range files {
		// 校验文件条目
		if f.Path == "" {
			_ = zw.Close()
			err = fmt.Errorf("empty zip source path")
			return err
		}
		if f.Name == "" {
			_ = zw.Close()
			err = fmt.Errorf("empty zip entry name")
			return err
		}

		// 打开源文件
		in, openErr := os.Open(f.Path)
		if openErr != nil {
			_ = zw.Close()
			err = openErr
			return err
		}

		// 在 ZIP 中创建条目并写入内容
		entry, createErr := zw.Create(f.Name)
		if createErr != nil {
			_ = in.Close()
			_ = zw.Close()
			err = createErr
			return err
		}
		if _, copyErr := io.Copy(entry, in); copyErr != nil {
			_ = in.Close()
			_ = zw.Close()
			err = copyErr
			return err
		}
		_ = in.Close()
	}

	// 关闭 ZIP writer，完成压缩包结构
	if closeErr := zw.Close(); closeErr != nil {
		err = closeErr
		return err
	}

	// 刷新缓冲区（如果启用了缓冲写入）
	if bw != nil {
		if flushErr := bw.Flush(); flushErr != nil {
			err = flushErr
			return err
		}
	}

	// 强制刷盘，确保数据持久化
	if syncErr := outFile.Sync(); syncErr != nil {
		err = syncErr
		return err
	}
	if closeErr := outFile.Close(); closeErr != nil {
		err = closeErr
		return err
	}
	closed = true

	// 原子重命名：将临时文件移动到目标路径
	if opts.Overwrite {
		_ = os.Remove(destPath) // Windows 兼容：先删除已存在的目标文件
	}
	if renameErr := os.Rename(tmpPath, destPath); renameErr != nil {
		err = renameErr
		return err
	}
	return nil
}
