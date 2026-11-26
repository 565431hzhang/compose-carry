import argparse
import base64
import json
import os
import pathlib
import platform
import subprocess
import tempfile
import time
import uuid

IMAGE = 'docker:29.0.4-dind'
ROOT = '/carry'
LIMIT = 16384


def command(args, data=None, expected=0, timeout=60):
    result = subprocess.run(args, input=data, capture_output=True, timeout=timeout)
    if result.returncode != expected:
        raise RuntimeError(f'{args[:5]}: exit {result.returncode}, wanted {expected}\n' + result.stdout.decode(errors='replace')[-4000:] + result.stderr.decode(errors='replace')[-4000:])
    return result.stdout


class Servers:
    def __init__(self, state, binary, arch):
        self.state, self.binary, self.arch = state, binary, arch
        self.names = []

    def execute(self, name, args, data=None, expected=0, user='1000:1000'):
        return command(['docker', 'exec', '-i', '--user', user, name] + args, data, expected)

    def shell(self, name, script, user='0:0'):
        return self.execute(name, ['sh', '-eu', '-c', script], user=user)

    def start(self, role):
        name = 'carry-ci-' + role + '-' + uuid.uuid4().hex
        self.names.append(name)
        self.state.write_text(json.dumps(self.names))
        command(['docker', 'run', '-d', '--privileged', '--name', name,
                 '--entrypoint', 'dockerd', IMAGE, '--host=unix:///var/run/docker.sock',
                 '--storage-driver=vfs', '--iptables=false', '--bridge=none'], timeout=90)
        deadline = time.monotonic() + 90
        while True:
            result = subprocess.run(['docker', 'exec', name, 'docker', 'info'], capture_output=True, timeout=15)
            if result.returncode == 0:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError('DinD daemon did not become ready')
            time.sleep(1)
        command(['docker', 'cp', str(self.binary), name + ':/usr/local/bin/compose-carry'])
        self.shell(name, 'chmod 755 /usr/local/bin/compose-carry; mkdir /carry; chown 1000:1000 /carry; chmod 666 /var/run/docker.sock')
        self.shell(name, 'test "$(id -u)" = 1000; test "$(awk \'/CapEff/ {print $2}\' /proc/self/status)" = 0000000000000000', user='1000:1000')
        actual = self.execute(name, ['uname', '-m']).decode().strip()
        assert actual == {'amd64': 'x86_64', 'arm64': 'aarch64'}[self.arch], actual
        self.execute(name, ['compose-carry', '--version'])
        print(f'Native {self.arch} {role}: non-root scanner, zero effective capabilities', flush=True)
        return name

    def put(self, name, filename, data):
        # Filenames are fixed test constants, never supplied by a snapshot.
        self.execute(name, ['sh', '-c', 'umask 077; cat > /carry/' + filename], data)

    def get(self, name, filename):
        return self.execute(name, ['cat', '/carry/' + filename])

    def source(self, name):
        self.shell(name, 'mkdir /carry/data /carry/locked; touch /carry/config /carry/locked/file; chmod 700 /carry/locked; chown 1000:1000 /carry/data /carry/config')
        config = {'name': 'carry-ci', 'services': {'app': {'image': 'busybox:latest', 'volumes': [
            {'type': 'bind', 'source': '/carry/data', 'target': '/data'},
            {'type': 'bind', 'source': '/carry/locked/file', 'target': '/locked'}], 'configs': ['settings']}},
            'configs': {'settings': {'file': '/carry/config'}}}
        self.put(name, 'compose.json', json.dumps(config).encode())
        rendered = self.execute(name, ['docker', 'compose', '-f', '/carry/compose.json', 'config', '--format', 'json'])
        self.put(name, 'rendered.json', rendered)
        self.execute(name, ['compose-carry', 'source', '--config', '/carry/rendered.json', '--out', '/carry/source.json'], expected=1)
        full = json.loads(self.get(name, 'source.json'))
        locked = [e for e in full['entries'] if e['dependency']['path'] == '/carry/locked/file']
        assert len(locked) == 1 and locked[0]['source']['state'] == 'permission_denied'
        # Export a separate fully verified source snapshot for cross-host checks.
        config['services']['app']['volumes'] = config['services']['app']['volumes'][:1]
        self.put(name, 'compose-clean.json', json.dumps(config).encode())
        rendered = self.execute(name, ['docker', 'compose', '-f', '/carry/compose-clean.json', 'config', '--format', 'json'])
        self.put(name, 'clean.json', rendered)
        self.execute(name, ['compose-carry', 'source', '--config', '/carry/clean.json', '--out', '/carry/export.json'])
        snapshot = self.get(name, 'export.json')
        assert len(snapshot) <= LIMIT
        return snapshot

    def target(self, name, snapshot):
        self.put(name, 'source.json', snapshot)
        self.shell(name, 'touch /carry/data; chown 1000:1000 /carry/data')
        self.check(name, 'gaps', 1, {'target_missing', 'type_mismatch'})
        self.shell(name, 'rm /carry/data; mkdir /carry/data; touch /carry/config; chown 1000:1000 /carry/data /carry/config')
        self.check(name, 'fixed', 0, {'matched'})
        self.shell(name, 'mv /carry/data /carry/moved')
        self.put(name, 'paths.json', b'{"/carry/data":"/carry/moved"}')
        self.check(name, 'mapped', 0, {'matched'}, mapped=True)
        self.shell(name, 'rm /carry/config; ln -s /carry/moved /carry/config')
        self.check(name, 'linked', 1, {'matched', 'target_unverified'}, mapped=True)
        print('PASS: missing/type mismatch, repaired paths, exact mapping and symlink rejection', flush=True)

    def check(self, name, output, code, states, mapped=False):
        args = ['compose-carry', 'target', '--manifest', '/carry/source.json', '--out', '/carry/' + output + '.json']
        if mapped:
            args += ['--path-map', '/carry/paths.json']
        self.execute(name, args, expected=code)
        report = json.loads(self.execute(name, ['compose-carry', 'compare', '--source', '/carry/source.json', '--target', '/carry/' + output + '.json', '--format', 'json'], expected=code))
        assert len(report['results']) == 2 and {r['status'] for r in report['results']} == states
        assert report['exit_code'] == code
        if mapped:
            assert any(r['target_path'] == '/carry/moved' for r in report['results'])

    def resources(self, source, target):
        self.execute(source, ['docker', 'volume', 'create', 'carry-ci-volume'])
        self.execute(source, ['docker', 'network', 'create', '--internal', 'carry-ci-network'])
        config = {'services': {'app': {'image': 'busybox:latest', 'volumes': [{'type': 'volume', 'source': 'data', 'target': '/data'}], 'networks': ['edge']}},
                  'volumes': {'data': {'external': True, 'name': 'carry-ci-volume'}},
                  'networks': {'edge': {'external': True, 'name': 'carry-ci-network'}}}
        for name, code, status in [(source, 0, 'present'), (target, 1, 'missing')]:
            self.put(name, 'resources.json', json.dumps(config).encode())
            report = json.loads(self.execute(name, ['compose-carry', 'resources', '--config', '/carry/resources.json', '--docker-context', 'default', '--format', 'json'], expected=code))
            assert report['summary'][status] == 2
        print('PASS: independent Docker daemons expose different external resources', flush=True)


def cleanup(state):
    if not state.exists():
        return
    names = json.loads(state.read_text())
    for name in names:
        assert name.startswith('carry-ci-') and all(c.isalnum() or c == '-' for c in name)
        existing = command(['docker', 'ps', '-aq', '--filter', 'name=^/' + name + '$']).strip()
        if existing:
            command(['docker', 'rm', '-fv', name], timeout=60)
        assert not command(['docker', 'ps', '-aq', '--filter', 'name=^/' + name + '$']).strip()
    state.unlink()
    print('CLEANED: owned DinD containers and anonymous volumes', flush=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('mode', choices=['source', 'target', 'cleanup'])
    parser.add_argument('--arch', choices=['amd64', 'arm64'])
    args = parser.parse_args()
    state = pathlib.Path(os.environ['CARRY_CI_STATE'])
    if args.mode == 'cleanup':
        cleanup(state)
        return
    assert platform.machine() == {'amd64': 'x86_64', 'arm64': 'aarch64'}[args.arch]
    assert command(['go', 'env', 'GOARCH']).decode().strip() == args.arch
    commit = command(['git', 'rev-parse', 'HEAD']).decode().strip()
    assert not state.exists(), 'Unclean previous test state'
    command(['docker', 'pull', IMAGE], timeout=180)
    with tempfile.TemporaryDirectory(prefix='carry-native-') as directory:
        binary = pathlib.Path(directory) / 'compose-carry'
        env = dict(os.environ, CGO_ENABLED='0')
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/compose-carry'], env=env, check=True, timeout=120)
        servers = Servers(state, binary, args.arch)
        try:
            if args.mode == 'source':
                source, target = servers.start('source'), servers.start('target')
                snapshot = servers.source(source)
                servers.target(target, snapshot)
                servers.resources(source, target)
                payload = base64.b64encode(json.dumps({'arch': args.arch, 'commit': commit, 'snapshot': json.loads(snapshot)}, separators=(',', ':')).encode()).decode()
                assert len(payload) <= LIMIT
                with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
                    output.write('snapshot=' + payload + '\n')
            else:
                payload = os.environ['SOURCE_SNAPSHOT']
                assert 0 < len(payload) <= LIMIT
                decoded = base64.b64decode(payload, validate=True)
                assert len(decoded) <= LIMIT
                envelope = json.loads(decoded)
                assert envelope['arch'] == {'amd64': 'arm64', 'arm64': 'amd64'}[args.arch]
                assert envelope['commit'] == commit
                snapshot = envelope['snapshot']
                assert snapshot['version'] == 1 and snapshot['phase'] == 'source'
                assert len(snapshot['entries']) == 2
                assert {e['dependency']['path'] for e in snapshot['entries']} == {'/carry/data', '/carry/config'}
                servers.target(servers.start('cross-target'), json.dumps(snapshot).encode())
                print(f'PASS: actual {envelope["arch"]} source snapshot checked on native {args.arch}', flush=True)
        finally:
            cleanup(state)


if __name__ == '__main__':
    main()
