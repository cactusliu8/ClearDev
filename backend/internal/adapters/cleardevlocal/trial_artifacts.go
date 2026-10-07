package cleardevlocal

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// Executed after the application command, by the backend, inside the same
// disposable container. No shell, host path, or candidate-provided reader.
const trialArtifactReader = `const fs=require('node:fs'),crypto=require('node:crypto');
const names=JSON.parse(process.argv[1]);let remaining=65536;const result=[];
for(const name of names){

 const dirs=[];let fd;
 try {
 let parent=fs.openSync('/workspace',fs.constants.O_RDONLY|fs.constants.O_DIRECTORY|fs.constants.O_NOFOLLOW);dirs.push(parent);
 const parts=name.split('/');
 for(const part of parts.slice(0,-1)){
  parent=fs.openSync('/proc/self/fd/'+parent+'/'+part,fs.constants.O_RDONLY|fs.constants.O_DIRECTORY|fs.constants.O_NOFOLLOW);dirs.push(parent);
 }
 fd=fs.openSync('/proc/self/fd/'+parent+'/'+parts.at(-1),fs.constants.O_RDONLY|fs.constants.O_NOFOLLOW|fs.constants.O_NONBLOCK);
 try {const st=fs.fstatSync(fd);if(!st.isFile()||st.size>remaining)throw Error('artifact exceeds limit or is not a file');
 const buffer=Buffer.alloc(remaining+1);let length=0,n;while(length<buffer.length&&(n=fs.readSync(fd,buffer,length,buffer.length-length,null))>0)length+=n;
 if(length>remaining)throw Error('artifact grew beyond limit');remaining-=length;const bytes=buffer.subarray(0,length);
 result.push({path:name,base64:bytes.toString('base64'),sha256:crypto.createHash('sha256').update(bytes).digest('hex')});
 }finally{fs.closeSync(fd);}
 }finally{for(const dir of dirs.reverse())fs.closeSync(dir);}
}process.stdout.write(JSON.stringify(result));`

func readTrialArtifacts(ctx context.Context, container string, paths []string) ([]core.TrialArtifact, error) {
	payload, err := json.Marshal(paths)
	if err != nil {
		return nil, err
	}
	argv, err := CheckContainerExecArgs(container, []string{"node", "-e", trialArtifactReader, string(payload)})
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "docker", argv...) //nolint:gosec // backend-owned reader and validated container/paths, no shell.
	bounded := newCheckContainerOutputCollector(128*1024, func() {})
	command.Stdout, command.Stderr = bounded, bounded
	if err := command.Run(); err != nil {
		return nil, errors.New("STAGE_TRIAL_ARTIFACT_UNAVAILABLE: declared output file missing, redirected, unreadable or too large")
	}
	var out []core.TrialArtifact
	if bounded.Exceeded() || json.Unmarshal([]byte(bounded.String()), &out) != nil || len(out) != len(paths) {
		return nil, errors.New("STAGE_TRIAL_ARTIFACT_INVALID")
	}
	return out, nil
}
