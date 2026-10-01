package diarize

import (
	"fmt"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
)

// StreamConfigFor is the Stream configuration meeting settings ask for, for
// a meeting in lang: the speaker model chosen for that language (see
// models.Status.SpeakerModelFor) with its own-diarizer settings.
func StreamConfigFor(m config.MeetingConfig, status *models.Status, lang string) (StreamConfig, error) {
	if !status.SpeakerSegmentationReady {
		return StreamConfig{}, fmt.Errorf("speaker segmentation model not downloaded")
	}
	sm, path, _ := status.SpeakerModelFor(m.SpeakerModel, lang)
	if !status.SpeakerModelReady(sm) {
		return StreamConfig{}, fmt.Errorf("speaker model %s not downloaded", sm.Name)
	}
	params := DefaultParams()
	params.Threshold, params.MergeSimilarity = sm.StreamThreshold, sm.StreamMerge
	recluster := m.DiarizeRecluster
	if recluster <= 0 {
		recluster = 10
	}
	return StreamConfig{
		SegmentationModel: status.SpeakerSegmentationPath,
		EmbeddingModel:    path,
		Threads:           1, // all on the Stream's low-priority thread
		Stride:            max(1, m.DiarizeStride),
		ReclusterSeconds:  recluster,
		Params:            params,
	}, nil
}
