package model

// TavernPreset：酒馆原始导入的**元数据**（列表用；正文只从下载口按字节取）。
type TavernPreset struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"created_at"`
}
