package task

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTaskSnapshotV2HasNoLegacyPlatformSpecificTopLevelFields(t *testing.T) {
	typeOfSnapshot := reflect.TypeOf(TaskSnapshot{})
	fields := make(map[string]string, typeOfSnapshot.NumField())
	for index := 0; index < typeOfSnapshot.NumField(); index++ {
		field := typeOfSnapshot.Field(index)
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		fields[jsonName] = field.Name
	}
	for _, denied := range []string{
		"source",
		"quality",
		"filePath",
		"fileName",
		"fileSize",
		"downloaded",
		"decodeKey",
		"isAlbum",
		"albumTotal",
		"albumCompleted",
	} {
		if goField, exists := fields[denied]; exists {
			t.Errorf("v2 TaskSnapshot exposes legacy field %q via %s", denied, goField)
		}
	}
}

func TestTaskSnapshotPersistsPrivateAdapterAndRecoveryData(t *testing.T) {
	snapshot := TaskSnapshot{
		ID:                  "task-1",
		PlatformID:          PlatformWeChat,
		PlatformDataVersion: 3,
		PlatformData:        json.RawMessage(`{"url":"https://private.example/?token=secret"}`),
		PlatformCheckpoint: &PlatformCheckpointEnvelope{
			Version: 2,
			Data:    json.RawMessage(`{"resumeToken":"secret"}`),
		},
		PublishIntent: &PublishIntent{
			Generation:       4,
			TemporaryPath:    "private.part",
			PlannedFinalPath: "final.mp4",
			Draft: TaskArtifactDraft{
				ID:      "primary",
				Size:    1,
				SHA256:  strings.Repeat("a", 64),
				Primary: true,
			},
		},
	}
	serialized, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"platformData", "platformCheckpoint", "publishIntent", "private.part"} {
		if !strings.Contains(string(serialized), required) {
			t.Errorf("persisted v2 snapshot lost %q: %s", required, serialized)
		}
	}
}

func TestCloneSnapshotDeepCopiesAllMutableRecoveryData(t *testing.T) {
	original := TaskSnapshot{
		PlatformData: json.RawMessage(`{"url":"https://private.example/?token=original"}`),
		PlatformCheckpoint: &PlatformCheckpointEnvelope{
			Version: 2,
			Data:    json.RawMessage(`{"cursor":"original"}`),
		},
		Artifacts: []TaskArtifact{{
			ID:       "artifact-original",
			Metadata: map[string]string{"token": "artifact-original"},
		}},
		LastError: &TaskError{
			Code:     "error.original",
			Metadata: map[string]string{"token": "error-original"},
		},
		PublishIntent: &PublishIntent{
			TemporaryPath: "original.part",
			Draft: TaskArtifactDraft{
				ID:       "draft-original",
				Metadata: map[string]string{"token": "publish-original"},
			},
		},
	}
	clone := CloneSnapshot(original)

	if &clone.PlatformData[0] == &original.PlatformData[0] ||
		clone.PlatformCheckpoint == original.PlatformCheckpoint ||
		&clone.PlatformCheckpoint.Data[0] == &original.PlatformCheckpoint.Data[0] ||
		&clone.Artifacts[0] == &original.Artifacts[0] ||
		clone.LastError == original.LastError ||
		clone.PublishIntent == original.PublishIntent {
		t.Fatal("CloneSnapshot retained a mutable pointer or slice alias")
	}

	clone.PlatformData[0] = 'X'
	clone.PlatformCheckpoint.Data[0] = 'Y'
	clone.Artifacts[0].ID = "artifact-clone"
	clone.Artifacts[0].Metadata["token"] = "artifact-clone"
	clone.LastError.Code = "error.clone"
	clone.LastError.Metadata["token"] = "error-clone"
	clone.PublishIntent.TemporaryPath = "clone.part"
	clone.PublishIntent.Draft.ID = "draft-clone"
	clone.PublishIntent.Draft.Metadata["token"] = "publish-clone"

	if string(original.PlatformData) != `{"url":"https://private.example/?token=original"}` ||
		string(original.PlatformCheckpoint.Data) != `{"cursor":"original"}` ||
		original.Artifacts[0].ID != "artifact-original" ||
		original.Artifacts[0].Metadata["token"] != "artifact-original" ||
		original.LastError.Code != "error.original" ||
		original.LastError.Metadata["token"] != "error-original" ||
		original.PublishIntent.TemporaryPath != "original.part" ||
		original.PublishIntent.Draft.ID != "draft-original" ||
		original.PublishIntent.Draft.Metadata["token"] != "publish-original" {
		t.Fatalf("mutating the clone changed the original snapshot: %#v", original)
	}
}
