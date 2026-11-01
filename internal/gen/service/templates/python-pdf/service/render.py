"""Render a report template to HTML and to PDF.

Only the render child imports this module, after it sets its memory limit.
"""

from typing import Any

from jinja2 import FileSystemLoader, StrictUndefined
from jinja2.sandbox import SandboxedEnvironment
from weasyprint import HTML

from service.fetcher import AssetFetcher
from service.limits import ASSETS_DIR, TEMPLATE_SUFFIX, TEMPLATES_DIR, template_path

environment = SandboxedEnvironment(
    loader=FileSystemLoader(TEMPLATES_DIR),
    autoescape=True,
    undefined=StrictUndefined,
)


class UnknownTemplate(LookupError):
    """The template name is not valid or the template file does not exist."""


def render_html(template: str, data: dict[str, Any]) -> str:
    """Render the template with the variable data. The output is escaped HTML."""
    path = template_path(template)
    if path is None or not path.is_file():
        raise UnknownTemplate(template)
    return environment.get_template(template + TEMPLATE_SUFFIX).render(data=data)


def render_pdf(template: str, data: dict[str, Any]) -> bytes:
    """Render the template to PDF. Only data URLs and local assets load."""
    html = render_html(template, data)
    base_url = ASSETS_DIR.as_uri() + "/"
    return HTML(string=html, base_url=base_url, url_fetcher=AssetFetcher()).write_pdf()
