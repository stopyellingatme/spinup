//! SvelteKit page generation.
use crate::pages::{answer, capitalize, exists};
use anyhow::{Result, bail};
use spinup_gen::contract::{Generator, Input, Output};
use spinup_vfs::plan::File;

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static PAGE: Generator = Generator {
    name: "page",
    aliases: &[],
    short: "Generate a SvelteKit page in web/src/routes",
    usage: "<route>",
    args: |cmd| {
        cmd.arg(
            clap::Arg::new("server")
                .long("server")
                .action(clap::ArgAction::SetTrue)
                .help("also write +page.server.ts with a load function"),
        )
    },
    run: plan,
};

fn static_segment(s: &str) -> bool {
    !s.is_empty()
        && s.split('-').all(|p| {
            !p.is_empty()
                && p.bytes()
                    .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit())
        })
}
fn group(s: &str) -> bool {
    s.strip_prefix('(')
        .and_then(|s| s.strip_suffix(')'))
        .is_some_and(static_segment)
}
fn param(s: &str) -> bool {
    s.strip_prefix('[')
        .and_then(|s| s.strip_suffix(']'))
        .is_some_and(|s| {
            !s.is_empty()
                && s.starts_with(|c: char| c.is_ascii_alphabetic() || c == '_')
                && s.bytes().all(|c| c.is_ascii_alphanumeric() || c == b'_')
        })
}

/// Validates a SvelteKit route and returns its segments.
///
/// # Errors
/// Returns an error for an empty, invalid, or reserved API route.
pub fn parse_route(route: &str) -> Result<Vec<String>> {
    let text = route.trim_matches('/');
    if text.is_empty() {
        bail!(
            "route {route:?} is empty: the home page is web/src/routes/+page.svelte\nTry: spinup g page about"
        );
    }
    let segments: Vec<_> = text.split('/').map(String::from).collect();
    for s in &segments {
        if !static_segment(s) && !param(s) && !group(s) {
            bail!(
                "route {route:?} has the invalid segment {s:?}: use lower-case letters, digits, and hyphens, a parameter such as [id], or a group such as (app)\nTry: spinup g page settings/profile"
            );
        }
    }
    if segments
        .iter()
        .find(|s| !group(s))
        .is_some_and(|s| s == "api")
    {
        bail!(
            "route {route:?} is under /api, which goes to the API\nTry: choose another route, or add an API operation in api/openapi/"
        );
    }
    Ok(segments)
}

fn plan(input: &Input<'_>) -> Result<Output> {
    if input.args.len() != 1 {
        bail!(
            "expected one route\nTry: spinup g page <route> (for example: spinup g page settings/profile)"
        );
    }
    if !input.lock.packs.iter().any(|p| p == "web") {
        bail!(
            "the app has no web pack, so it has no web/src/routes\nTry: generate pages in an app made without --api-only"
        );
    }
    let segments = parse_route(&input.args[0])?;
    let server = input
        .matches
        .try_get_one::<bool>("server")
        .ok()
        .flatten()
        .copied()
        .unwrap_or(false);
    if server && answer(input.lock, "frontend_mode").as_deref() == Some("embed") {
        bail!(
            "cannot generate a server page in embed mode: +page.server.ts needs a SvelteKit server\nTry: omit --server, or use an app with --frontend-mode node"
        );
    }
    let header = exists(input.root, "web/src/lib/components/app/page-header.svelte")?;
    let dir = format!("web/src/routes/{}", segments.join("/"));
    let name = segments
        .iter()
        .rev()
        .find(|s| static_segment(s))
        .map(|s| capitalize(&s.replace('-', " ")))
        .unwrap_or_else(|| "Page".into());
    let mut output = Output::default();
    output.plan.add(File {
        path: format!("{dir}/+page.svelte"),
        data: svelte(&dir, &name, server, header).into_bytes(),
        class: "scaffold".into(),
        ..File::default()
    })?;
    if server {
        output.plan.add(File {
            path: format!("{dir}/+page.server.ts"),
            data: server_ts(&name, input.lock.packs.iter().any(|p| p == "auth")).into_bytes(),
            class: "scaffold".into(),
            ..File::default()
        })?;
    }
    output.next.push(format!(
        "cd web && {}",
        spinup_project::webtool::Tool::from_lock(Some(input.lock)).run("check", &[])
    ));
    Ok(output)
}

fn svelte(dir: &str, name: &str, server: bool, header: bool) -> String {
    let mut b = String::new();
    let mut edit = format!("<code>{dir}/+page.svelte</code>");
    if server {
        edit.push_str(" and <code>+page.server.ts</code>");
    }
    if server || header {
        b.push_str("<script lang=\"ts\">\n");
        if header {
            b.push_str("\timport PageHeader from '#lib/components/app/page-header.svelte';\n");
        }
        if server {
            b.push_str("\timport type { PageProps } from './$types';\n\n\tlet { data }: PageProps = $props();\n");
        }
        b.push_str("</script>\n\n");
    }
    b += &format!("<svelte:head>\n\t<title>{name}</title>\n</svelte:head>\n\n");
    if header && server {
        b.push_str("<PageHeader title={data.title} />\n");
    } else if header {
        b += &format!("<PageHeader title=\"{name}\" />\n");
    } else {
        b += &format!(
            "<h1 class=\"mb-2 text-2xl font-semibold\">{}</h1>\n",
            if server { "{data.title}" } else { name }
        );
    }
    b += &format!(
        "<p class=\"text-muted-foreground text-sm\">\n\tEdit {edit} to change this page.\n</p>\n"
    );
    b
}
fn server_ts(name: &str, auth: bool) -> String {
    let (imports, args, guard) = if auth {
        (
            "import { requireUser } from '#lib/server/auth.js';\n",
            "{ locals, url }",
            "\trequireUser(locals, url);\n",
        )
    } else {
        ("", "", "")
    };
    format!(
        "{imports}import type {{ PageServerLoad }} from './$types';\n\n// load runs on the server for each request of this page. To read from the API, use the\n// typed client with the fetch of the load function:\n//\n//     const {{ data }} = await api.GET('/api/...', {{ fetch, baseUrl: __PUBLIC_ORIGIN__ }});\nexport const load: PageServerLoad = ({args}) => {{\n{guard}\treturn {{ title: '{name}' }};\n}};\n"
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn page_route_paths() {
        for route in [
            "about",
            "/settings/profile/",
            "posts/[slug]",
            "(marketing)/pricing-plans",
            "v2",
            "[id]",
            "(app)",
        ] {
            assert!(parse_route(route).is_ok(), "{route}");
        }
        for route in [
            "",
            "/",
            "About",
            "a//b",
            "../x",
            "+page",
            "a_b",
            "-a",
            "[1id]",
            "api/x",
            "(app)/api",
        ] {
            let error = parse_route(route).unwrap_err().to_string();
            assert!(error.contains("\nTry: "), "{error}");
        }
    }
    #[test]
    fn header_and_server_bytes() {
        assert_eq!(
            svelte("web/src/routes/about", "About", false, false),
            "<svelte:head>\n\t<title>About</title>\n</svelte:head>\n\n<h1 class=\"mb-2 text-2xl font-semibold\">About</h1>\n<p class=\"text-muted-foreground text-sm\">\n\tEdit <code>web/src/routes/about/+page.svelte</code> to change this page.\n</p>\n"
        );
        assert!(svelte("x", "About", true, true).contains("<PageHeader title={data.title} />"));
        assert!(svelte("x", "About", true, false).contains(">{data.title}</h1>"));
        assert!(svelte("x", "About", false, true).contains("<PageHeader title=\"About\" />"));
        assert!(server_ts("About", true).contains("\n\trequireUser(locals, url);\n"));
        assert!(!server_ts("About", false).contains("requireUser"));
    }
}
