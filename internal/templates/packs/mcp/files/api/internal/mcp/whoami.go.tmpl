package mcp

import (
	"context"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
	kitmcp "github.com/stopyellingatme/spinup/kit/x/mcp"
)

type whoamiOutput struct {
	UserID   int64  `json:"user_id"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	KeyName  string `json:"key_name"`
	KeyScope string `json:"key_scope"`
	PlanName string `json:"plan_name"`
}

func registerWhoami(s *kitmcp.Server) {
	kitmcp.Tool(s, kitmcp.ToolDef{
		Name:        "whoami",
		Description: "Returns the authenticated user and API key.",
		ReadOnly:    true,
	}, func(ctx context.Context, _ struct{}) (whoamiOutput, error) {
		user, _ := auth.UserFrom(ctx)
		key, _ := apikeys.KeyFrom(ctx)
		return whoamiOutput{
			UserID:   user.ID,
			Email:    user.Email,
			Role:     user.Role,
			KeyName:  key.Key.Name,
			KeyScope: string(key.Key.Scope),
			PlanName: key.PlanName,
		}, nil
	})
}
