// Build-only transformation; original project attribution stays in LICENSE/NOTICE.
export function transformGameDemoHtml(html: string): string {
  return html.replaceAll('元发问数平台', 'GameShelf 游戏经营助手')
    .replaceAll('/yanfachatbi-logo.svg', '/gameshelf-logo.svg')
    .replaceAll('/yanfachatbi-banner.png', '/gameshelf-banner.svg')
}
