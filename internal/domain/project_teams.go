package domain

// ProjectTeamRelationship grants ownership access only in its exact project.
// It is deliberately separate from organization membership and role subjects.
type ProjectTeamRelationship struct {
	ProjectID ProjectID `json:"projectId"`
	TeamID    TeamID    `json:"teamId"`
}
