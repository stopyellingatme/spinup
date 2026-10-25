//! The Go tests of `internal/doctor/composelint`, ported.

use super::*;
use std::path::Path;

fn count(findings: &[Finding], rule: &str) -> usize {
    findings.iter().filter(|f| f.rule == rule).count()
}

#[test]
fn lint_rules() {
    let tests: &[(&str, &str, &str, usize)] = &[
        (
            "privileged positive",
            "services:\n  app:\n    privileged: true\n",
            "privileged",
            1,
        ),
        (
            "privileged negative",
            "services:\n  app:\n    privileged: false\n",
            "privileged",
            0,
        ),
        (
            "socket short",
            "services:\n  app:\n    volumes: [/var/run/docker.sock:/sock]\n",
            "docker-socket",
            1,
        ),
        (
            "socket long",
            "services:\n  app:\n    volumes:\n      - type: bind\n        source: /run/docker.sock\n        target: /sock\n",
            "docker-socket",
            1,
        ),
        (
            "socket negative",
            "services:\n  app:\n    volumes: [/var/run/data:/data]\n",
            "docker-socket",
            0,
        ),
        (
            "host namespace",
            "services:\n  app:\n    network_mode: host\n    pid: host\n    ipc: host\n",
            "host-namespace",
            3,
        ),
        (
            "namespace negative",
            "services:\n  app:\n    network_mode: bridge\n",
            "host-namespace",
            0,
        ),
        (
            "dangerous capability",
            "services:\n  app:\n    cap_add: [all, cap_sys_admin]\n",
            "dangerous-cap",
            2,
        ),
        (
            "safe capability",
            "services:\n  app:\n    cap_add: [NET_BIND_SERVICE]\n",
            "dangerous-cap",
            0,
        ),
        (
            "unconfined",
            "services:\n  app:\n    security_opt: [seccomp:unconfined, apparmor=unconfined]\n",
            "unconfined",
            2,
        ),
        (
            "confined",
            "services:\n  app:\n    security_opt: [no-new-privileges:true]\n",
            "unconfined",
            0,
        ),
        (
            "public port short",
            "services:\n  app:\n    ports: [5432:5432, '5433']\n",
            "public-port",
            2,
        ),
        (
            "public port long",
            "services:\n  app:\n    ports:\n      - target: 5432\n        published: '5432'\n        host_ip: 0.0.0.0\n",
            "public-port",
            1,
        ),
        (
            "private ports",
            "services:\n  app:\n    ports: ['127.0.0.1:5432:5432', '[::1]:5433:5433']\n",
            "public-port",
            0,
        ),
        (
            "public label",
            "services:\n  proxy:\n    labels: [spinup.public=true]\n    ports: ['443:443']\n",
            "public-port",
            0,
        ),
        (
            "unpinned image",
            "services:\n  app:\n    image: alpine:latest\n",
            "unpinned-image",
            1,
        ),
        (
            "pinned image",
            "services:\n  app:\n    image: alpine@sha256:abc\n",
            "unpinned-image",
            0,
        ),
        (
            "build image",
            "services:\n  app:\n    image: local:dev\n    build: .\n",
            "unpinned-image",
            0,
        ),
        (
            "literal secret map",
            "services:\n  app:\n    environment:\n      PASSWORD: value\n      TOKEN_FILE: /run/token\n",
            "literal-secret",
            1,
        ),
        (
            "literal secret list",
            "services:\n  app:\n    environment: ['API_KEY=value', 'PRIVATE_KEY=${KEY}', 'SECRET=$SECRET']\n",
            "literal-secret",
            1,
        ),
        (
            "secret default",
            "services:\n  app:\n    environment:\n      PASSWORD: ${PASSWORD:-default}\n",
            "literal-secret",
            1,
        ),
    ];
    for (name, data, rule, want) in tests {
        let findings = lint("deploy/compose.yml", data).unwrap();
        assert_eq!(count(&findings, rule), *want, "{name}: {findings:#?}");
    }
}

#[test]
fn suppression_sorting_and_yaml_error() {
    let data = "services:\n  zebra: # spinup:allow privileged intended for tests\n    privileged: true\n  alpha:\n    image: alpine:latest\n    # spinup:allow public-port\n    ports: ['80:80']\n";
    let findings = lint("deploy/compose.yml", data).unwrap();
    assert_eq!(findings.len(), 3, "{findings:#?}");
    assert_eq!(findings[0].service, "alpha");
    assert_eq!(findings[0].rule, "bad-suppression");
    assert_eq!(findings[1].rule, "public-port");
    assert_eq!(findings[2].rule, "unpinned-image");
    assert_eq!(count(&findings, "privileged"), 0);
    assert!(lint("deploy/compose.yml", "services: [").is_err());
}

#[test]
fn compose_security_check() {
    let root = std::env::temp_dir().join(format!("spinup-doctor-compose-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    std::fs::create_dir_all(&root).unwrap();
    let ctx = Context::new(&root);
    assert_eq!(run_check(&ctx).status, Status::Skip);

    let deploy = root.join("deploy");
    std::fs::create_dir(&deploy).unwrap();
    let file = deploy.join("compose.yaml");
    std::fs::write(&file, "services:\n  app:\n    image: alpine@sha256:abc\n").unwrap();
    assert_eq!(run_check(&ctx).status, Status::Ok);

    let mut services = String::from("services:\n");
    for i in 0..11u8 {
        services.push_str(&format!(
            "  app{}:\n    privileged: true\n",
            (b'a' + i) as char
        ));
    }
    std::fs::write(&file, services).unwrap();
    let result = run_check(&ctx);
    assert_eq!(result.status, Status::Fail);
    assert_eq!(
        result.message.matches('\n').count(),
        10,
        "{}",
        result.message
    );
    assert!(result.message.contains("1 more"));
    assert!(!result.try_hint.is_empty());
    let _ = std::fs::remove_dir_all(&root);
}

#[test]
fn registered_check() {
    let check = crate::registry::checks(Scope::Local, true)
        .into_iter()
        .find(|c| c.id == "compose-security")
        .expect("compose-security is registered");
    assert_eq!(check.title, "Compose security lint");
    assert!(check.quick);
}

#[test]
fn review_regressions() {
    let tests: &[(&str, &str, &str, &str, usize)] = &[
        ("title bool", "", "privileged: True", "privileged", 1),
        ("upper bool", "", "privileged: TRUE", "privileged", 1),
        ("string bool", "", "privileged: 'true'", "privileged", 0),
        ("yaml 1.1 bool", "", "privileged: yes", "privileged", 0),
        (
            "privileged interpolation",
            "",
            "privileged: ${PRIV:-true}",
            "privileged",
            1,
        ),
        (
            "namespace interpolation",
            "",
            "network_mode: ${NET:-host}\n    pid: $PID\n    ipc: ${IPC}",
            "host-namespace",
            3,
        ),
        (
            "public address",
            "",
            "ports: ['203.0.113.5:80:80']",
            "public-port",
            1,
        ),
        (
            "private network address",
            "",
            "ports: ['192.168.1.1:80:80']",
            "public-port",
            1,
        ),
        (
            "interpolated host",
            "",
            "ports: ['${BIND:-0.0.0.0}:80:80']",
            "public-port",
            1,
        ),
        (
            "interpolated loopback",
            "",
            "ports: ['${BIND:-127.0.0.1}:80:80']",
            "public-port",
            1,
        ),
        (
            "public ipv6",
            "",
            "ports: ['[2001:db8::1]:80-90:80-90/udp']",
            "public-port",
            1,
        ),
        (
            "loopback forms",
            "",
            "ports: ['127.8.9.10:80-90:80-90/udp', '[::1]:80:80', 'localhost:80:80', '127.0.0.1:${PORT:-80}:80']",
            "public-port",
            0,
        ),
        (
            "public long",
            "",
            "ports: [{host_ip: '203.0.113.5', target: 80}]",
            "public-port",
            1,
        ),
        (
            "private long",
            "",
            "ports: [{host_ip: '::1', target: 80}, {host_ip: '[::1]', target: 81}]",
            "public-port",
            0,
        ),
        (
            "public label",
            "",
            "labels: {spinup.public: 'true'}\n    ports: ['203.0.113.5:80:80']",
            "public-port",
            0,
        ),
        (
            "default colon",
            "",
            "environment: {DB_PASSWORD: '${DB_PASSWORD:-hunter2}'}",
            "literal-secret",
            1,
        ),
        (
            "default dash",
            "",
            "environment: ['DB_PASSWORD=${DB_PASSWORD-hunter2}']",
            "literal-secret",
            1,
        ),
        (
            "empty defaults",
            "",
            "environment: {PASSWORD: '${PASSWORD:-}', TOKEN: '${TOKEN-}'}",
            "literal-secret",
            0,
        ),
        (
            "required secret",
            "",
            "environment: {PASSWORD: '${PASSWORD:?required}'}",
            "literal-secret",
            0,
        ),
        (
            "dev default yml",
            "deploy/compose.dev.yml",
            "environment: {PASSWORD: '${PASSWORD:-hunter2}'}",
            "literal-secret",
            0,
        ),
        (
            "dev default yaml",
            "deploy/compose.dev.yaml",
            "environment: {PASSWORD: '${PASSWORD-hunter2}'}",
            "literal-secret",
            0,
        ),
        (
            "dev literal",
            "compose.dev.yml",
            "environment: {PASSWORD: hunter2}",
            "literal-secret",
            1,
        ),
        (
            "url credentials",
            "",
            "environment: {DATABASE_URL: 'postgres://app:hunter2@db/app'}",
            "literal-secret",
            1,
        ),
        (
            "url list",
            "",
            "environment: ['DATABASE_URL=postgres://app:hunter2@db/app']",
            "literal-secret",
            1,
        ),
        (
            "url interpolation",
            "",
            "environment: {DATABASE_URL: 'postgres://app:${PASSWORD}@db/app'}",
            "literal-secret",
            0,
        ),
        (
            "url mixed password",
            "",
            "environment: {DATABASE_URL: 'postgres://app:literal${PASSWORD}@db/app'}",
            "literal-secret",
            1,
        ),
        (
            "url default",
            "",
            "environment: {DATABASE_URL: 'postgres://app:${PASSWORD:-hunter2}@db/app'}",
            "literal-secret",
            1,
        ),
        (
            "url dev default",
            "compose.dev.yml",
            "environment: {DATABASE_URL: 'postgres://app:${PASSWORD:-app}@db/app'}",
            "literal-secret",
            0,
        ),
        (
            "socket parent",
            "",
            "volumes: ['/var/run:/host/run']",
            "docker-socket",
            1,
        ),
        (
            "socket root",
            "",
            "volumes: ['/:/host']",
            "docker-socket",
            1,
        ),
        (
            "socket clean",
            "",
            "volumes: ['/var/lib/../run/:/host/run']",
            "docker-socket",
            1,
        ),
        (
            "socket long parent",
            "",
            "volumes: [{type: bind, source: /run/, target: /host/run}]",
            "docker-socket",
            1,
        ),
        (
            "socket sibling",
            "",
            "volumes: ['/var/runtime:/host/run', '/run/docker.sock.backup:/backup']",
            "docker-socket",
            0,
        ),
    ];
    for (name, file, data, rule, want) in tests {
        let file = if file.is_empty() {
            "deploy/compose.yml"
        } else {
            file
        };
        let findings = lint(file, &format!("services:\n  app:\n    {data}\n")).unwrap();
        assert_eq!(count(&findings, rule), *want, "{name}: {findings:#?}");
        if name.contains("interpolation") && *rule != "literal-secret" {
            for f in findings.iter().filter(|f| f.rule == *rule) {
                assert!(f.message.contains("interpolat"), "{name}: {}", f.message);
            }
        }
    }
}

#[test]
fn merge_and_alias() {
    let tests: &[(&str, &str, usize, usize)] = &[
        (
            "merge",
            "x-d: &d {privileged: true, network_mode: host}\nservices:\n  app:\n    <<: *d\n",
            1,
            1,
        ),
        (
            "service alias",
            "x-d: &d {privileged: true, network_mode: host}\nservices:\n  app: *d\n",
            1,
            1,
        ),
        (
            "sequence precedence",
            "x-a: &a {privileged: false}\nx-b: &b {privileged: true, network_mode: host}\nservices:\n  app:\n    <<: [*a, *b]\n",
            0,
            1,
        ),
        (
            "own precedence",
            "x-d: &d {privileged: true, network_mode: host}\nservices:\n  app:\n    privileged: false\n    <<: *d\n    network_mode: bridge\n",
            0,
            0,
        ),
        (
            "own suppression",
            "x-d: &d {privileged: true, network_mode: host}\nservices:\n  app:\n    # spinup:allow privileged needed for tests\n    <<: *d # spinup:allow host-namespace needed for tests\n",
            0,
            0,
        ),
        (
            "nested alias",
            "x-p: &p TRUE\nx-d: &d {privileged: *p}\nservices:\n  app:\n    <<: *d\n",
            1,
            0,
        ),
        (
            "nested merge",
            "x-a: &a {privileged: true}\nx-b: &b {<<: *a, network_mode: host}\nservices:\n  app:\n    <<: *b\n",
            1,
            1,
        ),
    ];
    for (name, data, privileged, host) in tests {
        let findings = lint("compose.yml", data).unwrap();
        assert_eq!(
            count(&findings, "privileged"),
            *privileged,
            "{name}: {findings:#?}"
        );
        assert_eq!(
            count(&findings, "host-namespace"),
            *host,
            "{name}: {findings:#?}"
        );
    }
}

#[test]
fn cycle_does_not_hang() {
    // The parser can reject the cycle. Either answer is fine, but the lint must return.
    let data = "x: &a [*a]\nservices:\n  app:\n    privileged: true\n";
    if let Ok(findings) = lint("compose.yml", data) {
        assert_eq!(count(&findings, "privileged"), 1);
    }
}

#[test]
fn clean_path_matches_go() {
    for (input, want) in [
        ("", "."),
        ("/", "/"),
        ("/var/run/", "/var/run"),
        ("/var/lib/../run/", "/var/run"),
        ("a//b/./c", "a/b/c"),
        ("../a", "../a"),
        ("/..", "/"),
        ("a/..", "."),
    ] {
        assert_eq!(clean_path(input), want, "{input}");
    }
}

#[test]
fn golden_dev_compose() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let file = "testdata/golden/api/deploy/compose.dev.yml";
    let data = std::fs::read_to_string(root.join(file)).unwrap();
    let findings = lint(file, &data).unwrap();
    assert!(findings.is_empty(), "{findings:#?}");
}

#[test]
fn item_comment_suppresses_only_that_item() {
    // (name, data, rule, want findings)
    let tests: &[(&str, &str, &str, usize)] = &[
        (
            "trailing comment on one volume",
            "services:\n  app:\n    volumes:\n      - /var/run/docker.sock:/a # spinup:allow docker-socket needed here\n      - /run/docker.sock:/b\n",
            "docker-socket",
            1,
        ),
        (
            "head comment on one volume",
            "services:\n  app:\n    volumes:\n      # spinup:allow docker-socket needed here\n      - /var/run/docker.sock:/a\n      - /run/docker.sock:/b\n",
            "docker-socket",
            1,
        ),
        (
            "comment on the second volume",
            "services:\n  app:\n    volumes:\n      - /var/run/docker.sock:/a\n      - /run/docker.sock:/b # spinup:allow docker-socket needed here\n",
            "docker-socket",
            1,
        ),
        (
            "both volumes suppressed",
            "services:\n  app:\n    volumes:\n      - /var/run/docker.sock:/a # spinup:allow docker-socket one\n      - /run/docker.sock:/b # spinup:allow docker-socket two\n",
            "docker-socket",
            0,
        ),
        (
            "item comment does not suppress another rule",
            "services:\n  app:\n    privileged: true\n    volumes:\n      - /data:/d # spinup:allow privileged wrong place\n",
            "privileged",
            1,
        ),
        (
            "item comment on a long form port",
            "services:\n  app:\n    ports:\n      # spinup:allow public-port intended\n      - target: 80\n        published: 80\n      - target: 81\n        published: 81\n",
            "public-port",
            1,
        ),
        (
            "environment entry",
            "services:\n  app:\n    environment:\n      PASSWORD: a # spinup:allow literal-secret test value\n      TOKEN: b\n",
            "literal-secret",
            1,
        ),
        (
            "environment list entry",
            "services:\n  app:\n    environment:\n      - PASSWORD=a # spinup:allow literal-secret test value\n      - TOKEN=b\n",
            "literal-secret",
            1,
        ),
        (
            "capability list",
            "services:\n  app:\n    cap_add:\n      - ALL # spinup:allow dangerous-cap reason\n      - SYS_ADMIN\n",
            "dangerous-cap",
            1,
        ),
        (
            "indentless list",
            "services:\n  app:\n    volumes:\n    - /var/run/docker.sock:/a # spinup:allow docker-socket reason\n    - /run/docker.sock:/b\n",
            "docker-socket",
            1,
        ),
        (
            "key comment still covers the service",
            "services:\n  app:\n    volumes: # spinup:allow docker-socket reason\n      - /var/run/docker.sock:/a\n      - /run/docker.sock:/b\n",
            "docker-socket",
            0,
        ),
        (
            "a comment without a reason on an item suppresses nothing",
            "services:\n  app:\n    volumes:\n      - /var/run/docker.sock:/a # spinup:allow docker-socket\n",
            "docker-socket",
            1,
        ),
    ];
    for (name, data, rule, want) in tests {
        let findings = lint("deploy/compose.yml", data).unwrap();
        assert_eq!(count(&findings, rule), *want, "{name}: {findings:#?}");
    }
}

#[test]
fn anchor_comments_reach_the_service() {
    let tests: &[(&str, &str, &str, usize)] = &[
        (
            "merge copies a commented key",
            "x-d: &d\n  # spinup:allow privileged reason\n  privileged: true\nservices:\n  app:\n    <<: *d\n",
            "privileged",
            0,
        ),
        (
            "alias service copies a commented key",
            "x-d: &d\n  privileged: true # spinup:allow privileged reason\nservices:\n  app: *d\n",
            "privileged",
            0,
        ),
        (
            "an overridden key does not bring its comment",
            "x-d: &d\n  privileged: true # spinup:allow privileged reason\nservices:\n  app:\n    privileged: true\n    <<: *d\n",
            "privileged",
            1,
        ),
        (
            "item comment in an anchor",
            "x-d: &d\n  volumes:\n    - /var/run/docker.sock:/a # spinup:allow docker-socket reason\n    - /run/docker.sock:/b\nservices:\n  app:\n    <<: *d\n",
            "docker-socket",
            1,
        ),
        (
            "an uncommented anchor still fails",
            "x-d: &d\n  privileged: true\nservices:\n  app:\n    <<: *d\n",
            "privileged",
            1,
        ),
        (
            "suppression in another service does not leak",
            "services:\n  a:\n    privileged: true # spinup:allow privileged reason\n  b:\n    privileged: true\n",
            "privileged",
            1,
        ),
    ];
    for (name, data, rule, want) in tests {
        let findings = lint("deploy/compose.yml", data).unwrap();
        assert_eq!(count(&findings, rule), *want, "{name}: {findings:#?}");
    }
}
