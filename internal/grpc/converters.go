package grpcserver

import (
	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

func pbCommandType(t pb.CommandType) nodecommand.CommandType {
	if t == pb.CommandType_COMMAND_TYPE_CUSTOM {
		return nodecommand.CommandTypeCustom
	}
	return nodecommand.CommandTypeDefault
}

func pbJobStatus(s pb.JobStatus) string {
	switch s {
	case pb.JobStatus_JOB_STATUS_RUNNING:
		return string(job.StatusRunning)
	case pb.JobStatus_JOB_STATUS_FAILED:
		return string(job.StatusFailed)
	default:
		return string(job.StatusCompleted)
	}
}
