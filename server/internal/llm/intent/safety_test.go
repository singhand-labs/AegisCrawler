package intent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterCandidates_RemovesUnsafe(t *testing.T) {
	candidates := []Candidate{
		{ID: "c1", Label: "正常采集", Description: "抓取商品信息"},
		{ID: "c2", Label: "窃取密码", Description: "获取用户敏感信息"},
		{ID: "c3", Label: "破解账号", Description: "暴力破解登录"},
	}
	filtered := FilterCandidates(candidates)
	assert.Len(t, filtered, 1)
	assert.Equal(t, "c1", filtered[0].ID)
}

func TestFilterCandidates_AllowsSafe(t *testing.T) {
	candidates := []Candidate{
		{ID: "c1", Label: "采集标题", Description: "抓取商品标题"},
	}
	filtered := FilterCandidates(candidates)
	assert.Len(t, filtered, 1)
}

func TestFilterCandidates_EmptyInput(t *testing.T) {
	filtered := FilterCandidates(nil)
	assert.Empty(t, filtered)
}

func TestFilterCandidates_CaseInsensitive(t *testing.T) {
	candidates := []Candidate{
		{ID: "c1", Label: "获取PASSWORD", Description: "抓取公开数据"},
	}
	filtered := FilterCandidates(candidates)
	assert.Empty(t, filtered)
}
