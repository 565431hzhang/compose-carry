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
        config = {'name': 'carry-ci', 'services': {'app': {'image': 'busybox:latest',
            'environment': {'PASSWORD': 'private-ci-not-exported'}, 'volumes': [
            {'type': 'bind', 'source': '/carry/data', 'target': '/data'},
            {'type': 'bind', 'source': '/carry/locked/file', 'target': '/locked'},
            {'type': 'volume', 'source': 'external', 'target': '/external'}],
            'configs': ['settings'], 'networks': ['edge']}},
            'configs': {'settings': {'file': '/carry/config'}},
            'volumes': {'external': {'external': True, 'name': 'carry-ci-volume'}},
            'networks': {'edge': {'external': True, 'name': 'carry-ci-network'}}}
        self.put(name, 'compose.json', json.dumps(config).encode())
        rendered = self.execute(name, ['docker', 'compose', '-f', '/carry/compose.json', 'config', '--format', 'json'])
        self.put(name, 'rendered.json', rendered)
        summary = self.execute(name, ['compose-carry', 'export', '--config', '/carry/rendered.json', '--out', '/carry/locked.json'])
        assert b'source_unverified=1' in summary
        full = json.loads(self.get(name, 'locked.json'))
        locked = [e for e in full['paths']['entries'] if e['dependency']['path'] == '/carry/locked/file']
        assert len(locked) == 1 and locked[0]['source']['state'] == 'permission_denied'
        config['services']['app']['volumes'].pop(1)
        self.put(name, 'compose-clean.json', json.dumps(config).encode())
        rendered = self.execute(name, ['docker', 'compose', '-f', '/carry/compose-clean.json', 'config', '--format', 'json'])
        self.put(name, 'clean.json', rendered)
        self.shell(name, 'cd /carry; /usr/local/bin/compose-carry export --config clean.json', user='1000:1000')
        bundle = self.get(name, 'carry.json')
        assert len(bundle) <= LIMIT and b'private-ci-not-exported' not in bundle and b'PASSWORD' not in bundle
        print('PASS: single export bundle retains source evidence without configuration secrets', flush=True)
        return bundle

    def target(self, name, bundle):
        self.put(name, 'carry.json', bundle)
        self.execute(name, ['test', '!', '-e', '/carry/clean.json'])
        self.shell(name, 'touch /carry/data; chown 1000:1000 /carry/data')
        base = ['compose-carry', 'check', '--bundle', '/carry/carry.json', '--format', 'json']
        no_context = json.loads(self.execute(name, base, expected=1))
        assert no_context['resources']['summary']['unverified'] == 2
        assert all(r['reason'] == 'context_required' for r in no_context['resources']['results'])
        args = base + ['--docker-context', 'default']
        gaps = json.loads(self.execute(name, args, expected=1))
        assert {r['status'] for r in gaps['paths']['results']} == {'target_missing', 'type_mismatch', 'excluded'}
        assert gaps['resources']['summary']['missing'] == 2
        self.shell(name, 'rm /carry/data; mkdir /carry/data; touch /carry/config; chown 1000:1000 /carry/data /carry/config')
        self.execute(name, ['docker', 'volume', 'create', 'carry-ci-volume-renamed'])
        self.execute(name, ['docker', 'network', 'create', '--internal', 'carry-ci-network-renamed'])
        self.put(name, 'names.json', json.dumps({'volumes': {'carry-ci-volume': 'carry-ci-volume-renamed'}, 'networks': {'carry-ci-network': 'carry-ci-network-renamed'}}).encode())
        args += ['--resource-map', '/carry/names.json']
        repaired = json.loads(self.execute(name, args))
        assert repaired['exit_code'] == 0 and repaired['paths']['summary']['matched'] == 2
        assert repaired['resources']['summary']['present'] == 2 and repaired['resources']['results'] == []
        assert len(repaired['paths']['results']) == 1 and repaired['paths']['results'][0]['status'] == 'excluded'
        self.execute(name, args + ['--fail-on-excluded'], expected=1)
        self.shell(name, 'mv /carry/data /carry/moved')
        self.put(name, 'paths.json', b'{"/carry/data":"/carry/moved"}')
        args += ['--path-map', '/carry/paths.json']
        mapped = json.loads(self.execute(name, args + ['--all']))
        assert any(r['target_path'] == '/carry/moved' for r in mapped['paths']['results'])
        assert {r['checked_name'] for r in mapped['resources']['results']} == {'carry-ci-volume-renamed', 'carry-ci-network-renamed'}
        self.shell(name, 'rm /carry/config; ln -s /carry/moved /carry/config')
        linked = json.loads(self.execute(name, args, expected=1))
        assert linked['paths']['summary']['target_unverified'] == 1
        assert self.get(name, 'carry.json') == bundle
        self.execute(name, ['test', '!', '-e', '/carry/target.json'])
        print('PASS: unified check handles gaps, context, repairs, maps, strict exclusions and symlinks without target snapshots', flush=True)


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
                bundle = servers.source(source)
                servers.target(target, bundle)
                payload = base64.b64encode(json.dumps({'arch': args.arch, 'commit': commit, 'bundle': json.loads(bundle)}, separators=(',', ':')).encode()).decode()
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
                bundle = envelope['bundle']
                assert bundle['kind'] == 'compose-carry.bundle' and bundle['version'] == 1
                snapshot, resources = bundle['paths'], bundle['resources']
                assert snapshot['version'] == 1 and snapshot['phase'] == 'source'
                assert len(snapshot['entries']) == 3
                assert {e['dependency']['path'] for e in snapshot['entries']} == {'/carry/data', '/carry/config', ''}
                assert resources['kind'] == 'compose-carry.resources' and resources['version'] == 1
                assert {(r['kind'], r['name']) for r in resources['resources']} == {('volume', 'carry-ci-volume'), ('network', 'carry-ci-network')}
                servers.target(servers.start('cross-target'), json.dumps(bundle).encode())
                print(f'PASS: actual {envelope["arch"]} export bundle checked on native {args.arch}', flush=True)
        finally:
            cleanup(state)


if __name__ == '__main__':
    main()
