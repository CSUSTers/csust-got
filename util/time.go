package util

import (
    "csust-got/context"
    "github.com/golang/protobuf/ptypes/duration"
    "github.com/google/cel-go/cel"
    "github.com/google/cel-go/checker/decls"
    "time"
)


func EvalDuration(s string) (time.Duration, error) {
    env, err := cel.NewEnv(cel.Declarations(
        decls.NewIdent("time", decls.String, nil)))
    if err != nil {
        return 0, err
    }
    result, err := context.EvalCELWithVals(env, "duration(time)", map[string]interface{}{
        "time": s,
    })
    if err != nil {
        return 0, err
    }
    return time.Duration(result.(*duration.Duration).Seconds) * time.Second, nil
}