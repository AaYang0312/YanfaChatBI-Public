export const productName = '元发问数平台'

export const TECHNICAL_DETAILS_LABEL = '技术详情'
export const UNKNOWN_ERROR_SUMMARY = '服务暂时不可用，请稍后重试。'

function deepFreeze<T>(value: T): T {
  if (value && typeof value === 'object' && !Object.isFrozen(value)) {
    Object.freeze(value)
    for (const nested of Object.values(value as Record<string, unknown>)) {
      deepFreeze(nested)
    }
  }
  return value
}

function deepClone<T>(value: T): T {
  if (Array.isArray(value)) {
    return value.map(item => deepClone(item)) as T
  }
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {}
    for (const [key, nested] of Object.entries(value as Record<string, unknown>)) {
      out[key] = typeof nested === 'function' ? nested : deepClone(nested)
    }
    return out as T
  }
  return value
}

const syncReasons: Record<string, string> = {
  SYNC_FORBIDDEN: '当前账号没有启动同步的权限',
  SYNC_RELEASE_UNAVAILABLE: '同步组件尚未就绪',
  SOURCE_BUSY: '当前有问数任务，请结束后再同步',
  DATA_SYNC_IN_PROGRESS: '数据同步中',
  DATA_SYNC_RECOVERING: '数据源正在恢复收尾',
  REQUEST_CONFLICT: '请求标识与原同步操作冲突，请先查询原请求',
}

const catalog = {
  productName,
  sourceAccess: {
    syncing: '数据同步中，暂时无法问数',
    recovering: '数据同步正在恢复收尾，暂时无法读取数据源',
    unavailable: '同步状态暂不可确认，请稍后手动重试',
    unresolved: 'ERP 数据源绑定尚未确认，暂时无法读取数据源',
    changed: '账号、连接或数据源绑定已变化，请在当前连接重新提交',
  },
  dataSync: {
    menu: '手动同步数据',
    progress: '查看同步进度',
    title: '数据同步',
    scope: '已配置店铺的订单与售后增量',
    pause: '同步期间暂停同源问数，历史报告和草稿保留。关闭窗口不会取消同步或解除问数门禁。',
    start: '开始同步',
    newOperation: '开始新一轮同步',
    resend: '以原请求标识重发',
    refresh: '重新查询状态',
    pending: '正在确认原同步请求；只查询原请求，不会自动重发。',
    notFound: '尚未找到原请求。原 POST 仍可能迟到；如需重发，请明确使用同一请求标识。',
    storage: '无法安全保存同步请求，请允许本站本地存储后重新查询；未发送新的同步请求。',
    unavailable: '同步状态暂不可确认，请恢复网络后重新查询。原请求仍保留。',
    forbidden: '当前账号没有启动同步的权限，仍可查看同步进度。',
    notReady: '同步组件、配置或批准前置尚未就绪。',
    busy: '当前数据源忙碌，请结束已有任务后再同步。',
    preparing: '准备同步',
    syncing: '增量同步',
    maintenance: '外部增量维护进行中，正在等待安全收尾',
    maintenanceRef: '维护操作引用',
    verifying: '核验同步结果',
    succeeded: '同步成功',
    failed: '同步失败',
    interrupted: '同步已中断',
    releasing: '正在确认安全收尾，问数许可仍以数据源状态为准。',
    partial: '部分数据已更新，本次同步未全部完成；已提交的数据可能保留。',
    commitUnknown: '尚不能确认是否有数据已提交，部分数据可能已更新。',
    lastSuccess: '最近成功同步',
    started: '开始时间',
    noSuccess: '暂无成功记录',
    unknownTotal: '总店铺数尚不可确认',
    counts: (done: number, failed: number, processed: number, total: number | null) =>
      `确认完成 ${done} 家 / 失败 ${failed} 家；已处理 ${processed}${total === null ? '' : ` / ${total}`} 家店铺`,
    reason: (code: string) => syncReasons[code] ?? '同步前置尚未就绪',
  },
  common: {
    confirm: '确认',
    cancel: '取消',
    close: '关闭',
    save: '保存',
    create: '创建',
    delete: '删除',
    edit: '编辑',
    copy: '复制',
    copied: '已复制到剪贴板',
    loading: '加载中…',
    retry: '重试',
    pleaseWait: '请稍候…',
    or: '或',
    unknown: '未知',
    yes: '是',
    no: '否',
    back: '返回',
    next: '下一页',
    previous: '上一页',
    search: '搜索',
    settings: '设置',
    logout: '退出登录',
    sampleDatabase: '示例数据库',
    technicalDetails: TECHNICAL_DETAILS_LABEL,
  },
  dates: {
    today: '今天',
    yesterday: '昨天',
    justNow: '刚刚',
    minuteAgo: '分钟前',
    minutesAgo: '分钟前',
    hourAgo: '小时前',
    hoursAgo: '小时前',
    days7: '7 天内',
    days30: '30 天内',
    joined: '加入于',
    page: '第',
    pageOf: '页，共',
    of: '/',
    iso: 'ISO',
    human: '人读',
    secondsAgo: '秒前',
    daysAgo: '天前',
  },
  auth: {
    login: {
      title: productName,
      subtitle: `欢迎回来${productName}，请登录后继续`,
      usernamePlaceholder: '用户名或邮箱',
      passwordPlaceholder: '密码',
      usernameRequired: '请输入用户名或邮箱',
      usernameMin: '用户名或邮箱至少 3 个字符',
      passwordRequired: '请输入密码',
      passwordMin: '密码至少 6 个字符',
      submit: '登录',
      submitting: '正在登录…',
      forgot: '忘记密码？',
      switchToSignup: '新用户 — 注册',
      welcomeBack: (name: string) => `欢迎回来，${name}！`,
    },
    signup: {
      title: productName,
      subtitle: `创建账号，开始使用${productName}`,
      usernamePlaceholder: '用户名',
      emailPlaceholder: '邮箱',
      passwordPlaceholder: '密码',
      confirmPasswordPlaceholder: '确认密码',
      secretPlaceholder: '注册密钥',
      usernameRequired: '请输入用户名',
      usernameMin: '用户名至少 3 个字符',
      usernameNoSpace: '用户名不能包含空格',
      emailRequired: '请输入邮箱',
      emailInvalid: '请输入有效邮箱',
      passwordRequired: '请输入密码',
      passwordMin: '密码至少 6 个字符',
      passwordMismatch: '两次密码不一致',
      secretRequired: '请输入注册密钥',
      secretHint: '注册新用户需要密钥，请向管理员索取。',
      googleSecretHint: '请输入注册密钥以继续 Google 注册',
      googleSecretPlaceholder: '请输入注册密钥',
      secretInvalid: '注册密钥无效，请重试。',
      secretValid: '密钥有效，可以继续使用 Google 注册。',
      validating: '正在校验',
      validate: '校验',
      submit: '注册',
      submitting: '正在注册…',
      google: '使用 Google 继续',
      switchToLogin: '已有账号 — 登录',
      welcome: (name: string) => `欢迎加入${productName}，${name}！`,
    },
    forgot: {
      title: productName,
      subtitle: '输入邮箱以接收密码重置验证码',
      emailPlaceholder: '邮箱',
      emailRequired: '请输入邮箱',
      emailInvalid: '请输入有效邮箱',
      submit: '发送重置验证码',
      submitting: '正在发送验证码…',
      backToLogin: '返回登录',
      sendFailed: '发送重置邮件失败',
      sentIfExists: '如果账号存在，将收到重置邮件。',

    },
    reset: {
      title: productName,
      subtitle: '输入发送到邮箱的 6 位验证码（若未收到请查看垃圾箱）和新密码',
      emailPlaceholder: '邮箱',
      otpPlaceholder: '6 位验证码',
      newPasswordPlaceholder: '新密码',
      confirmPasswordPlaceholder: '确认新密码',
      otpRequired: '请输入验证码',
      otpLength: '验证码必须为 6 位数字',
      otpDigits: '验证码只能包含数字',
      submit: '重置密码',
      submitting: '正在重置密码…',
      failed: '重置密码失败',
      backToLogin: '返回登录',
    },
    google: {
      processing: '正在处理认证…',
      authenticating: '正在使用 Google 认证',
      successTitle: '成功！',
      success: '认证成功！',
      sheetsConnected: 'Google Sheets 已连接！',
      googleAuthSuccess: 'Google 认证成功！',
      redirecting: '正在跳转…',
      failedTitle: '认证失败',
      redirectingLogin: '正在跳转到登录页…',
      missingCode: 'OAuth 回调无效：缺少授权码',
      accountExists: '该邮箱已注册，正在跳转到登录页…',
      failedPrefix: '认证失败：',
    },
    loggedOut: '您已退出登录！',
  },
  app: {
    loadingTitle: `正在加载${productName}`,
    loadingSubtitle: '正在准备您的 AI 数据助手…',
    loadingWhat: `${productName}能做什么？`,
    useCases: [
      '分析销售：“看看本季度畅销商品”',
      '生成报告：“汇总各区域月度收入”',
      '识别趋势：“过去一年客户获取有何变化？”',
      '监控指标：“当前客户留存率是多少？”',
      '跟踪库存：“展示库存低于 10 件的商品”',
      '人事分析：“各部门员工流动率如何？”',
      '财务洞察：“对比一季度费用与年度预算”',
    ],
    welcomeTitle: `你问，${productName}会答！`,
    welcomeSubtitle1: '开源 AI 助手，让数据库交互更自然。',
    welcomeSubtitle2: '从 SQL 到 NoSQL，用自然对话探索并分析数据。',
    welcomeAskTitle: '用自然语言提问',
    welcomeAskBody: `用日常语言与数据库对话。${productName}会自动将问题转成查询。`,
    welcomeMultiTitle: '多数据源支持',
    welcomeMultiBody: '可连接 PostgreSQL、MySQL、MongoDB、Redis 等。一个界面管理全部数据库。',
    welcomeVizTitle: '可视化结果',
    welcomeVizBody: '以表格或 JSON 查看数据。执行查询并实时看结果。',
    createConnection: '新建连接',
    orSelectExisting: '或从侧栏选择已有连接开始',
  },
  sidebar: {
    connectionMenu: '连接菜单',
    noConnections: '暂无连接',
    addToStart: '添加连接以开始使用',
    howTo: `如何使用${productName}`,
    tutorial: '尝试快速教程',
    addConnection: '添加连接',
    collapse: '收起侧栏',
    expand: '展开侧栏',
    confirmLogout: '确认退出',
    logoutMessage: '确定要退出登录吗？未保存的更改将丢失。',
    copyLink: '复制链接',
    chatSettings: '对话设置',
    duplicateChat: '复制对话',
    deleteChat: '删除对话',
    signOut: '退出',
  },
  demo: {
    title: `${productName}使用教程`,
    loading: '正在加载使用教程…',
    loadHint: '若教程未显示，请检查网络后点击重试。',
    loadError: '教程暂时无法加载。请检查网络后点击重试，或关闭后稍后再试。',
  },
  chat: {
    analyzing: '正在分析您的请求…',
    welcome: (name: string) => `你好${name || '呀'}！我是你的数据助手。你可以问任何关于数据的问题，我会理解并回答。你可以从“有哪些数据”开始，或尝试推荐问题。`,
    tryAsking: '你可以试试：',
    noPinned: '暂无置顶消息',
    noPinnedHint: '将常用或重要消息置顶，以便快速访问',
    scrollBottom: '滚动到底部',
    loadingMore: '正在加载更多消息…',
    connecting: '正在连接…',
    connected: '已连接',
    disconnected: '已断开',
    reconnect: '重新连接',
    disconnect: '断开连接',
    searchMessages: '搜索消息',
    chatOptions: '对话选项',
    refreshKnowledge: '刷新知识库',
    showMessages: '显示消息',
    showPinned: '显示置顶',
    clearChat: '清空对话',
    placeholderConnected: '问问你的数据…',
    placeholderDisconnected: '尚未连接数据库…',
    selectModel: '选择 AI 模型',
    selectedModel: '已选：',
    modelsAvailable: '个模型可用',
    cancelRequest: '取消请求',
    sendMessage: '发送消息',
    searchPlaceholder: '搜索消息…',
    noResults: '无结果',
    previousResult: '上一个结果',
    nextResult: '下一个结果',
    closeSearch: '关闭搜索',
    recommendTitle: '不知道问什么？',
    recommendBody: '试试我们的推荐问题。',
    disconnectedTitle: '当前已断开',
    disconnectedBody: '点击上方的重连按钮以重新连接数据源。',
    copyMessage: '复制消息',
    pinMessage: '置顶消息',
    unpinMessage: '取消置顶',
    editMessage: '编辑消息',
    messagePinned: '消息已置顶',
    messageUnpinned: '消息已取消置顶',
    pinFailed: '更新置顶状态失败',
    timeout: '处理超时，请重试。',
    tryAgain: '重试',
    responseCompleted: '助手回复完成！',
    refreshKbTitle: '刷新知识库',
    refreshKbMessage: '将从数据源重新拉取结构并更新知识库。视数据量可能需要 2–3 分钟。',
    refresh: '刷新',
    clearTitle: '清空对话',
    clearMessage: '确定要清空全部对话消息吗？此操作不可撤销。',
    disconnectTitle: '断开连接',
    disconnectMessage: '确定要断开该数据库吗？以后可随时重连。',
    editQueryTitle: '编辑查询',
    editQueryMessage: '确定要编辑该查询吗？可能影响执行结果。',
  },
  query: {
    explanation: '解释',
    action: '操作',
    queryLabel: '查询',
    edited: '已编辑',
    rolledBackOn: '已回滚于',
    executedOn: '已执行于',
    executeManually: '手动执行',
    editQuery: '编辑查询',
    cancelQuery: '取消查询',
    rerunQuery: '重新执行',
    executeQuery: '执行查询',
    copyQuery: '复制查询',
    saveChanges: '保存修改',
    executing: '正在执行查询…',
    error: '错误',
    expand: '展开',
    collapse: '收起',
    exampleResult: '示例结果：',
    rolledBackResult: '回滚结果：',
    result: '结果：',
    tableView: '表格视图',
    jsonView: 'JSON 视图',
    visualizationView: '可视化视图',
    downloadData: '下载数据',
    exportHint: (total: number, pages: number) => `将导出 ${pages} 页中的 ${total} 条记录。`,
    exportCsv: '导出为 CSV',
    exportJson: '导出为 JSON',
    exportViz: '导出可视化',
    rollback: '回滚更改',
    copyJson: '复制 JSON',
    criticalTitle: '关键查询',
    criticalMessage: '该查询可能影响重要数据。确定要继续吗？',
    cancelled: '查询已取消',
    noData: '暂无数据',
    noDataDisplay: '暂无可展示数据',
    noExample: '暂无示例数据',
    previousPage: '上一页',
    nextPage: '下一页',
    toggleDate: '切换日期格式',
  },
  chart: {
    empty: '暂无可视化数据',
    errorTitle: '发生错误',
    loading: '正在渲染图表…',
    regenerate: '重新生成',
    regenerateHint: '使用最新数据重新生成图表。',
    unknownType: '未知图表类型：',
    pieMissing: '缺少饼图配置',
    heatmapMissing: '缺少热力图配置',
    funnelMissing: '缺少漏斗图配置',
    bubbleMissing: '缺少气泡图配置',
    waterfallMissing: '缺少瀑布图配置',
    noLabel: '无标签',
    series: '序列',
    noData: '暂无数据',
    frequency: '频次',
    count: '计数',
    dragRange: '拖动控件选择区间',
    low: '低',
    high: '高',
    noNumeric: '无可用于直方图的数值数据',
    toggleNumber: '切换数值格式',
    full: '完整',
    compact: 'K/M',
    asc: '升序',
    desc: '降序',
    sortDesc: '降序',
    clearSort: '清除排序',
    sortAsc: '升序',
    less: '收起',
    readMore: '展开',
    emptyList: '空列表',
    emptyObject: '空对象',
    showingRows: '行',
  },
  viz: {
    notSupported: '该查询结果不支持可视化。',
    notSupportedTitle: '不支持可视化',
    noData: '暂无可视化数据。',
    generated: '可视化已生成！',
    failed: '生成可视化失败。',
    failedPrefix: '无法生成可视化：',
    errorPrefix: '可视化错误：',
    loadFailed: '加载可视化数据失败',
    generating: '正在生成可视化…',
    couldNot: '无法生成可视化',
    ready: '可视化已就绪',
    clickToLoad: '点击下方按钮加载可视化',
    load: '加载可视化',
    title: '可视化查询结果',
    subtitle: 'AI 将分析数据并生成最合适的图表',
    generate: '生成可视化',
  },
  connection: {
    newTitle: '新建连接',
    settingsTitle: '对话设置',
    displayName: '名称',
    physicalDatabase: '物理数据库',
    encrypted: '数据源凭证以加密形式存储。',
    dataSourceType: '数据源类型',
    selectSource: '选择数据源',
    spreadsheetFiles: '电子表格文件（CSV、Excel）',
    googleSheets: 'Google Sheets',
    basic: '基本',
    sshTunnel: 'SSH 隧道',
    tabConnection: '连接',
    tabKnowledge: '知识库',
    tabSettings: '设置',
    passwordNotice: '重要：更新连接时必须重新输入数据库密码。',
    updating: '正在更新…',
    creating: '正在创建…',
    saveKnowledge: '保存知识库',
    uploadFiles: '上传文件',
    updateSettings: '更新设置',
    updateKnowledge: '更新知识库',
    updateConnection: '更新连接',
    refreshSheetsMessage: '将从 Google Sheets 重新拉取数据并更新知识库。视表格大小可能需要几分钟。',
    host: '主机',
    hostHint: '数据库服务器的主机名或 IP',
    hostPlaceholder: '例如 localhost、db.example.com、192.168.1.1',
    port: '端口',
    portHint: '数据库监听的端口号',
    portPlaceholder: '例如 5432（PostgreSQL）、3306（MySQL）、27017（MongoDB）',
    database: '数据库名',
    databaseHint: '要连接的具体数据库名称',
    databasePlaceholder: '例如 myapp_production、users_db',
    authDatabase: '认证数据库',
    authDatabaseHint: '用于认证的数据库（MongoDB 通常为 admin）',
    username: '用户名',
    usernameHint: '具有相应权限的数据库用户',
    password: '密码',
    passwordHint: '数据库用户密码',
    passwordPlaceholder: '请输入数据库密码',
    uriHint: '连接 URI 用于自动填充下方字段。请将 <password> 替换为实际密码。',
    spreadsheetNext: '请在下一步继续上传 CSV/Excel 文件。连接详情将自动处理。',
    sslTitle: 'SSL/TLS 安全',
    sslHint: '启用与数据库的加密连接',
    useSsl: '使用 SSL/TLS 加密',
    sslMode: 'SSL 模式',
    sslDisable: '禁用 — 不使用 SSL',
    sslRequire: '必需 — 仅加密',
    sslVerifyCa: '验证 CA',
    sslVerifyFull: '完整验证',
    sslCertUrl: 'SSL 证书 URL',
    sslKeyUrl: 'SSL 密钥 URL',
    sslRootUrl: 'SSL 根证书 URL',
    whitelistTitle: '白名单服务器地址',
    whitelistBody: '以下是发起数据源请求的服务器地址。若已启用 IP 限制，请将这些地址加入防火墙以允许连接。',
    copyAddress: '复制地址',
    sshTitle: 'SSH 隧道配置',
    sshHost: 'SSH 主机',
    sshPort: 'SSH 端口',
    sshUsername: 'SSH 用户名',
    sshAuthMethod: '认证方式',
    publicKey: '公钥',
    sshPrivateKey: 'SSH 私钥',
    sshKeyUrl: '或从 URL 加载私钥',
    sshPassphrase: 'SSH 口令（可选）',
    sshPassword: 'SSH 密码',
    credentials: '数据源凭证',
    autoFetch: '自动获取结果',
    autoFetchHint: 'AI 返回后自动从数据库获取操作结果。更新、插入、删除等关键操作仍需手动执行。',
    shareWithAi: '与 AI 共享结果',
    shareWithAiHint: `允许${productName}将查询结果提供给 AI 以改进回答，例如基于历史结果追问。注意：这会增加发送给 AI 的 token 与数据量。`,
    nonTech: '非技术模式',
    nonTechHint: '启用简化解释与结果。AI 会隐藏复杂查询，仅展示相关字段。适合业务用户。注意：不会应用于已有消息。',
    autoViz: '自动生成可视化',
    autoAnalyze: '自动解读执行结果',
    autoAnalyzeHint: '查询执行后，让模型基于真实结果给出解读。注意：每次执行会额外调用一次模型，仅在"自动获取结果"开启时生效。',
    generationModeHint: '仅生成模式：模型不会读取查询结果，只给出 SQL。',
    autoVizHint: '为适合可视化的查询结果自动生成图表。注意：会增加一定处理时间。',
    moreSettings: '更多设置即将上线，我们正在不断完善数据库连接配置选项。',
    detailsTitle: '连接详情',
    sslEnabled: '已启用 SSL',
    sslDisabled: '未启用 SSL',
    type: '类型',
    defaultSsl: '使用服务器默认 SSL 配置',
    validation: {
      hostRequired: '请输入主机',
      hostInvalid: '主机格式无效',
      portRequired: '请输入端口',
      portRange: '端口必须介于 1 与 65535 之间',
      databaseRequired: '请输入数据库名',
      databaseInvalid: '数据库名格式无效',
      usernameRequired: '请输入用户名',
      sslCertRequired: '当前 SSL 模式需要证书 URL',
      sslKeyRequired: '当前 SSL 模式需要密钥 URL',
      sslRootRequired: '当前 SSL 模式需要根证书 URL',
      urlInvalid: 'URL 格式无效',
      sshHostRequired: '请输入 SSH 主机',
      sshHostInvalid: 'SSH 主机格式无效',
      sshPortRequired: '请输入 SSH 端口',
      sshPortRange: 'SSH 端口必须介于 1 与 65535 之间',
      sshUsernameRequired: '请输入 SSH 用户名',
      sshKeyRequired: '公钥认证需要 SSH 私钥',
    },
  },
  schema: {
    loading: '正在加载知识库表…',
    loadingHint: '视数据库规模可能需要一些时间。',
    allAuto: '全部并自动检测',
    allAutoHint: '选择数据库中全部表/集合，并自动检测新增或删除的对象。',
    tables: '知识库表',
    all: '全部',
    selected: '已选',
    collapseAll: '全部收起',
    expandAll: '全部展开',
    selectHint: '或选择要纳入知识库的具体表。',
    search: '搜索表…',
    noMatch: '没有匹配的表',
    none: '数据库中没有表',
    columns: '列：',
    nullable: '可空',
    selectTables: '选择表/集合',
    loadingSchema: '正在加载结构…',
    saveApply: '保存并应用',
    saving: '正在保存…',
    atLeastOne: '至少需要选择一张表',
  },
  files: {
    existingTables: '已有数据表',
    rowsCols: '行 · 列',
    noExisting: '暂无已有表。',
    uploadMore: '上传更多文件',
    uploadCsv: '上传 CSV/Excel 文件',
    dropHint: '拖放 CSV 或 Excel 文件到此处，或点击浏览',
    processing: '正在处理…',
    choose: '选择文件',
    supported: '支持格式：CSV、XLSX、XLS（单文件最大 100MB）',
    uploaded: '已上传文件',
    tableName: '表名',
    tableNamePlaceholder: '请输入表名',
    tableNameHint: '这将作为数据库中的表名',
    security: '数据安全与隐私',
  },
  sheets: {
    howTo: '如何连接 Google Sheets：',
    authTitle: 'Google 认证',
    authHint: '使用 Google 认证以访问电子表格',
    authenticating: '正在认证…',
    authenticate: '使用 Google 认证',
    authenticated: '已认证',
    switch: '切换/重连',
    url: 'Google Sheets URL',
    urlLocked: '编辑已有连接时不能修改 URL',
    refreshData: '从 Google Sheets 刷新数据',
    connect: '连接 Google Sheets',
    open: '在 Google Sheets 中打开',
    info: '表格信息',
    sheetName: '表格名：',
    pages: '页数：',
    pageList: '页面：',
  },
  dataStructure: {
    loading: '正在加载知识库…',
    failed: '加载知识库失败',
    tryAgain: '重试',
    none: '暂无表。请上传 CSV/Excel 或同步 Google Sheets。',
    collapseTable: '收起表',
    expandTable: '展开表',
    downloadTable: '下载表数据',
    deleteTable: '删除表',
    downloadSelected: '下载选中行',
    deleteSelected: '删除选中行',
    actions: '操作',
    copyRow: '复制行数据',
    deleteRow: '删除行',
    rowsPerPage: '每页行数：',
    showing: '显示',
    firstPage: '首页',
    lastPage: '末页',
    downloadCsv: '下载为 CSV',
    downloadXlsx: '下载为 XLSX',
    deleteRowTitle: '删除行',
    deleteRowsTitle: '删除选中行',
    deleteTableTitle: '删除表',
  },
  dashboard: {
    chat: '对话',
    dashboard: '仪表盘',
    emptyTitle: '让数据动起来',
    emptySubtitle: 'AI 会为你生成仪表盘，用图表、指标和表格实时展现数据。',
    tryRecommendations: '尝试推荐',
    customizeWithAi: '用 AI 定制',
    orImport: '或导入仪表盘（.json）',
    select: '选择仪表盘',
    cancelRefresh: '取消刷新',
    refreshNow: '立即刷新',
    autoRefresh: '自动刷新间隔',
    more: '更多选项',
    new: '新建仪表盘',
    off: '关',
    addWidget: '添加组件',
    regenerate: '重新生成',
    importExport: '导入/导出',
    export: '导出',
    import: '导入',
    deleteDashboard: '删除仪表盘',
    loadingList: '正在加载仪表盘…',
    loadingOne: '正在加载仪表盘…',
    deleteConfirm: (name: string) => `确定要删除“${name}”吗？所有组件将被永久删除，且不可撤销。`,
    loadingData: '正在加载数据…',
    noData: '未找到数据',
    refreshWidget: '刷新组件数据',
    cancelLoading: '取消加载',
    lastRefreshed: '上次刷新：',
    editWidget: '编辑组件',
    removeWidget: '移除组件',
    remove: '移除',
    pickerHint: '选择要创建的仪表盘，以后仍可追加。',
    creating: '正在创建…',
    createN: '创建仪表盘',
    createOne: '创建仪表盘',
    customizeTitle: '用 AI 定制',
    customizeHint: '描述你想看到的仪表盘',
    insightsLabel: '你想看哪些洞察？',
    insightsPlaceholder: '例如：展示每日收入趋势、畅销商品和客户增长',
    insightsHelp: 'AI 将生成含组件、查询和图表配置的完整仪表盘。',
    generate: '生成',
    addTitle: '添加组件',
    addHint: '描述要添加的组件',
    addLabel: '告诉 AI 要添加什么组件',
    addPlaceholder: '例如：本月总收入、下单量前 10 用户、每日注册趋势',
    addHelp: 'AI 将自动生成查询、图表类型与配置。',
    adding: '正在添加组件…',
    add: '添加',
    editTitle: '编辑组件',
    editing: '正在编辑：',
    editLabel: '该组件应如何变化？',
    editPlaceholder: '例如：改为饼图、改为近 30 天、加趋势线',
    updating: '正在更新…',
    updateWidget: '更新组件',
    update: '更新',
    regenTitle: '选择重新生成原因',
    regenReason: '选择重新生成原因',
    tryAlternative: '尝试其他方案',
    tryAlternativeHint: '用不同指标与可视化生成新组件',
    applySchema: '应用新结构变更',
    applySchemaHint: '基于更新后的表与列重建仪表盘',
    extraInstructions: '补充说明（可选）',
    extraPlaceholder: '例如：关注用户参与指标',
    extraHelp: '告诉 AI 如何定制重新生成的仪表盘',
    importTitle: '导入仪表盘',
    importHint: '从 JSON 文件或粘贴内容导入仪表盘',
    pasteJson: '粘贴仪表盘 JSON',
    uploadJson: '上传 JSON 文件',
    clickSelect: '点击选择 JSON 文件',
    orDrop: '或拖放到此处',
    selectJson: '请选择 JSON 文件',
    pasteOrSelect: '请粘贴 JSON 或选择文件',
    invalidJson: 'JSON 格式无效',
    importing: '正在导入…',
    preparingSelected: '正在准备选中的仪表盘',
    preparingCustom: '正在准备定制仪表盘',
    curating: '正在整理推荐仪表盘',
    analyzing: '正在分析数据并构建组件…',
  },
  modal: {
    rollbackTitle: '回滚更改',
    rollbackMessage: '确定要回滚该查询做出的更改吗？将运行反向查询以撤销更改。',
    rollingBack: '正在回滚…',
    rollback: '回滚',
    deleteChat: '删除对话',
    deleteChatMessage: '确定要删除对话',
    cannotUndo: '此操作不可撤销。',
    typeToConfirm: '请输入以确认：',
    deleting: '正在删除…',
    duplicateTitle: '复制对话',
    duplicateMessage: '确定要复制吗？将创建相同数据库连接的新对话。',
    duplicateMessages: '复制全部消息',
    duplicateMessagesHint: '将对话历史复制到新对话。',
    duplicateDashboards: '复制全部仪表盘',
    duplicateDashboardsHint: '将仪表盘与组件复制到新对话。',
    duplicating: '正在复制…',
    duplicate: '复制',
    demoTitle: `使用演示`,
  },
  toasts: {
    kbRefreshed: '知识库已刷新',
    kbCancelled: '已取消知识库刷新',
    kbFailed: '刷新知识库失败：',
    sendFailed: '发送消息失败',
    editFailed: '编辑消息失败',
    queryUpdated: '查询已更新',
    queryUpdateFailed: '更新查询失败：',
    noErrorsToFix: '没有可修复的错误',
    retryMissing: '找不到可重试的原始消息',
    connectionAdded: '连接已添加',
    connectionDeleted: '连接已删除',
    connectionUpdated: '连接已更新！',
    updatingConnection: '正在更新连接…',
    reconnectManual: '自动重连失败，请手动重连。',
    reconnectNeeded: '连接详情已更新，请重新连接数据源。',
    updateConnectionFailed: '更新连接失败',
    chatDuplicated: '对话已复制！',
    chatDuplicatedNoConn: '对话已复制，但无法自动建立连接。',
    duplicateFailed: '复制对话失败：',
    updatingTables: '正在更新选中表…',
    updateTablesFailed: '更新表选择失败',
    reconnectFailed: '重新连接数据源失败：',
    loadTablesFailed: '加载表失败',
    settingsUpdated: '设置已更新',
    settingsFailed: '更新设置失败',
    uploadAtLeastOne: '请至少上传一个文件',
    authGoogleFirst: '请先完成 Google 认证并校验表格 URL',
    uploading: '正在上传文件…',
    uploadedLoading: '文件已上传，正在加载知识库表…',
    uploadedDone: '文件已上传并加载表',
    uploadFailed: '上传文件失败',
    establishing: '正在建立连接…',
    syncingSheets: '正在同步 Google Sheets 数据…',

    createdSelect: '连接已创建。请选择要纳入结构的表。',
    knowledgeSaved: '知识库已保存',
    knowledgeUpdated: '知识库表已更新',
    connectDbFailed: '连接数据库失败',
    linkCopied: '链接已复制',
    copyLinkFailed: '复制链接失败',
    sseFailed: '建立实时连接失败',
    queryExecuted: '查询已执行！',
    queryExecFailed: '查询执行失败：',
    changesReverted: '更改已撤销',
    fetchResultsFailed: '获取结果失败',
    noExportData: '没有可导出的数据',
    exportFailed: '导出数据失败：',
    vizNotFound: '未找到可视化或尚未加载',
    exportingViz: '正在导出可视化…',
    vizExported: '可视化已导出！',
    vizExportFailed: '导出可视化失败',
    loadPinnedFailed: '加载置顶消息失败',
    loadMessagesFailed: '加载消息失败',
    reconnecting: '正在重连…',
    reconnected: '已重新连接！',
    reconnectFail: '重连失败。',
    notActive: '连接未激活，请先重连。',
    loadDashboardFailed: '加载仪表盘失败',
    dashboardCreated: '仪表盘已创建！',
    dashboardRefreshing: '正在刷新仪表盘…',
    establishingConnections: '正在建立连接…',
    connectedRefreshing: '已连接！正在刷新…',
    connectionFailedManual: '连接失败，请手动重试。',
    unableConnect: '无法连接，请检查连接后重试。',
    refreshDashboardFailed: '刷新仪表盘失败',
    widgetTimeout: '加载超时，请刷新。',
    refreshFailed: '刷新失败',
  },
  sse: {
    fetchingData: '正在获取相关数据与结构…',
    searchingKb: '正在搜索知识库…',
    settingUpKb: '正在初始化知识库…',
    foundTables: (n: number) => `从知识库找到 ${n} 张相关表`,
    exploringKb: '正在探索知识库以发现相关上下文…',
    retrievingContext: '正在获取之前的对话上下文…',
    foundMessages: (n: number) => `找到 ${n} 条相关历史消息`,
    runningWorkflow: '正在运行工作流并获取结果…',
    toolError: (name: string) => `工具 ${name} 返回错误，正在重试…`,
    analyzingCriticality: '正在评估请求风险…',
    analyzingError: '正在分析错误以提供说明…',
    applyingFix: '查询出错，正在修复并重试…',
    fixApplied: '查询修复已应用！',
    combining: '正在整理并组装回复',
    generatingViz: '正在为结果生成可视化',
  },
  errors: {
    codes: {
      DATA_SYNC_IN_PROGRESS: '数据同步中，暂时无法问数',
      DATA_SYNC_RECOVERING: '数据同步正在恢复收尾，暂时无法读取数据源',
      SOURCE_UNRESOLVED: 'ERP 数据源绑定尚未确认，暂时无法读取数据源',
      SOURCE_STATE_UNAVAILABLE: '同步状态暂不可确认，请稍后手动重试',
      SYNC_STATE_UNAVAILABLE: '同步状态暂不可确认，请稍后手动重试',
      UNAUTHORIZED: '登录已失效，请重新登录。',
      FORBIDDEN: '没有权限执行该操作。',
      NOT_FOUND: '未找到请求的资源。',
      CONFLICT: '请求冲突，请刷新后重试。',
      VALIDATION_ERROR: '请检查输入后重试。',
      CONNECTION_ERROR: '数据库连接失败，请检查连接配置。',
      NO_CONNECTION_FOUND: '未找到可用连接。',
      NO_DRIVER_FOUND: '不支持的数据库类型。',
      QUERY_EXECUTION_FAILED: '查询执行失败。',
      QUERY_EXECUTION_TIMED_OUT: '查询执行超时。',
      QUERY_EXECUTION_CANCELLED: '查询已取消。',
      EXECUTION_CANCELLED: '执行已取消。',
      EXECUTION_ERROR: '执行出错。',
      TRANSACTION_ERROR: '事务执行失败。',
      SAFETY_VIOLATION: '查询被安全校验拦截。',
      FAILED_TO_START_TRANSACTION: '无法开始事务。',
      FAILED_TO_GET_SQL_CONNECTION: '无法获取数据库连接。',
      RESULT_PROCESSING_FAILED: '结果处理失败。',
      JSON_MARSHAL_FAILED: '结果序列化失败。',
      JSON_ERROR: '结果格式化失败。',
      INTERNAL_ERROR: '服务内部出错。',
      INVALID_QUERY: '查询格式无效。',
      INVALID_PARAMETERS: '参数无效。',
      UNSUPPORTED_OPERATION: '不支持的操作。',
      COLLECTION_NOT_FOUND: '集合不存在。',
      COLLECTION_EXISTS: '集合已存在。',
      TABLE_NOT_FOUND: '表不存在。',
      DECODE_ERROR: '解析结果失败。',
      MARSHAL_ERROR: '结果编码失败。',
      SCHEMA_SETUP_FAILED: '结构上下文设置失败。',
      AGGREGATION_RESULT_FORMAT_ERROR: '聚合结果格式化失败。',
      DUPLICATE_KEY: '记录已存在，不能重复添加。',
      invalid_schema: '请求格式无效。',
      invalid_date_range: '结束日期必须晚于开始日期',
      invalid_enum: '请选择支持的选项。',
      unknown_field: '请求包含未知字段。',
      ambiguous_product: '商品名称有多个候选，请先选择。',
      missing_field: '请补充必要信息。',
      conflicting_scope: '数据范围冲突。',
      unauthorized_scope: '没有权限访问该数据范围。',
      request_conflict: '请求冲突，请刷新后重试。',
      capability_unavailable: '当前数据源不支持该分析。',
      source_unavailable: '数据源暂不可用。',
      timeout: '分析超时。',
    },
    http: {
      400: '请求无效。',
      401: '登录已失效，请重新登录。',
      403: '没有权限执行该操作。',
      404: '未找到请求的资源。',
      409: '请求冲突，请刷新后重试。',
      422: '请检查输入后重试。',
      429: '请求过于频繁，请稍后重试。',
      500: '服务暂时不可用，请稍后重试。',
      502: '网关错误，请稍后重试。',
      503: '服务暂时不可用，请稍后重试。',
    } as Record<number, string>,
    messages: {
      'Invalid credentials, User does not exist.': '账号不存在。',
      'Invalid credentials. Please try again.': '账号或密码错误，请重试。',
      'User with this email already exists': '该邮箱已注册。',
      'User with this username already exists': '该用户名已注册。',
      'Invalid or expired OTP': '验证码无效或已过期。',
      'No data rows returned by the query. Visualization requires at least one data row.': '暂无可视化数据。',

      'Document with the same unique key already exists': '记录已存在，不能重复添加。',
    } as Record<string, string>,
  },
  assistant: {
    menu: {
      title: '工作流',
      close: '关闭工作流',
      productAnalysis: '商品跨店分析',
      salesComparison: '平台/店铺对比',
    },
    basis: {
      payment: '支付口径',
      outstock: '出库口径',
    },
    metrics: {
      quantity: '数量',
      sales: '销售额',
      cost: '成本',
      margin: '毛利',
      margin_rate: '毛利率',
      avg_price: '成交均价',
      refund: '退款',
    },
    coverage: {
      requested: '请求范围',
      expected: '预期范围',
      covered: '已覆盖',
      missing: '未采集',
      stale: '已过期',
      unknown: '质量未知',
      included: '已纳入',
      excluded: '已排除',
    },
    outcome: {
      complete: '完整',
      partial: '部分覆盖',
      empty: '无交易',
      unavailable: '不可用',
    },
    status: {
      created: '已创建',
      running: '计算中',
      completed: '已完成',
      failed: '失败',
      cancelled: '已取消',
      interrupted: '已中断',
    },
    card: {
      title: '经营报告',
      basis: '口径',
      window: '日期窗口',
      currency: '货币',
      dataAsOf: '数据截至',
      coverage: '覆盖',
      entity: '实体',
      refund: '退款',
      marginUnavailable: '不可计算',
      loadingNewBasis: '正在按新口径计算',
      switchBasis: '切换口径',
      unavailable: '报告暂不可用',
      missingTrend: '缺失趋势点',
      erpPilotCoverage: '仅覆盖试点内获批的 7 家店铺，其他店铺未覆盖。',
    },
    input: {
      title: '经营分析',
      natural: '自然语言',
      explicit: '明确条件',
      question: '问题',
      questionPlaceholder: '例如：近期杯子各店销量',
      workflow: '分析类型',
      productAnalysis: '商品分析',
      salesComparison: '销售对比',
      basis: '口径',
      startDate: '开始日期',
      endDate: '结束日期',
      scope: '范围',
      scopeAll: '全部已授权范围',
      scopeSelected: '指定范围',
      platformIds: '平台',
      shopIds: '店铺',
      idsPlaceholder: '多个标识用逗号分隔',
      product: '商品',
      productPlaceholder: '商品名称或已确认标识',
      productId: '商品标识',
      skuIds: 'SKU',
      group: '分组',
      platform: '平台',
      shop: '店铺',
      metrics: '指标',
      metricsNote: '当前数据源试点仅支持普通已支付销量（件）。',
      threshold: '低毛利阈值',
      thresholdPlaceholder: '例如 0.15，可留空',
      submit: '开始分析',
      submitting: '正在解析…',
      candidates: '请选择商品',
      missing: '请补充必要信息',
      reversedDate: '结束日期必须晚于开始日期',
      connectFirst: '请先连接数据源',
    },
    fields: {
      workflow: '分析类型',
      basis: '口径',
      period: '日期范围',
      'period.start_date': '开始日期',
      'period.end_date_exclusive': '结束日期',
      scope: '范围',
      product: '商品',
      'product.product_id': '商品标识',
      'product.sku_ids': 'SKU',
      group_by: '分组',
      metrics: '指标',
      low_margin_threshold: '低毛利阈值',
      product_query: '商品',
    } as Record<string, string>,
  },
}

export const zhCN = deepFreeze(catalog)

export function extendCopy<T extends Record<string, unknown>>(extra: T): Readonly<typeof catalog & T> {
  const merged = Object.assign(deepClone(catalog), extra)
  return deepFreeze(merged) as Readonly<typeof catalog & T>
}

export type ServiceErrorResult = {
  summary: string
  detail?: string
  code?: string
}

const SECRET_KEY = /(?:^|[\s_-])(?:api[\s_-]*key|access[\s_-]*key|secret[\s_-]*key|client[\s_-]*secret|password|passwd|pwd|secret|token|authorization|bearer)$/i
const REPORT_KEY = /(?:^|[\s_-])(?:revenue|amount|total|sales|profit|margin|net[\s_-]*income|gross[\s_-]*profit|region|account[\s_-]*name|customer[\s_-]*id|user[\s_-]*id|order[\s_-]*id|sku)$/i
const SECRET_NAME = /(?:api[\s_-]*key|access[\s_-]*key|secret[\s_-]*key|client[\s_-]*secret|password|passwd|pwd|secret|token|authorization|bearer)/i

function redactStructuredValue(value: unknown, key = ''): unknown {
  if (Array.isArray(value)) return value.map(item => redactStructuredValue(item))
  if (!value || typeof value !== 'object') {
    return SECRET_KEY.test(key) || REPORT_KEY.test(key) ? '[redacted]' : value
  }
  return Object.fromEntries(Object.entries(value).map(([childKey, childValue]) => [
    childKey,
    SECRET_KEY.test(childKey) || REPORT_KEY.test(childKey) ? '[redacted]' : redactStructuredValue(childValue, childKey),
  ]))
}

function redactStructuredText(text: string): string {
  let result = ''
  for (let index = 0; index < text.length; index += 1) {
    const opener = text[index]
    if (opener !== '{' && opener !== '[') {
      result += opener
      continue
    }
    const closer = opener === '{' ? '}' : ']'
    let depth = 0
    let inString = false
    let escaped = false
    let end = -1
    for (let cursor = index; cursor < text.length; cursor += 1) {
      const char = text[cursor]
      if (inString) {
        if (escaped) escaped = false
        else if (char === '\\') escaped = true
        else if (char === '"') inString = false
        continue
      }
      if (char === '"') inString = true
      else if (char === opener) depth += 1
      else if (char === closer) {
        depth -= 1
        if (depth === 0) {
          end = cursor
          break
        }
      }
    }
    const candidate = end === -1 ? opener : text.slice(index, end + 1)
    if (end === -1) {
      result += opener
      continue
    }
    try {
      result += JSON.stringify(redactStructuredValue(JSON.parse(candidate)))
      index = end
    } catch {
      result += opener
    }
  }
  return result
}


function redactSecretLiterals(text: string): string {
  const quoted = (body: string) => `'${body.replace(/'/g, "\\'")}'`
  let safe = text
    .replace(/\bbearer\s+[A-Za-z0-9._\-+/=]+/gi, '[redacted]')
    .replace(/\bsk-[A-Za-z0-9-]+/gi, '[redacted]')
  safe = safe.replace(
    new RegExp(String.raw`(${SECRET_NAME.source})(\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,}\]]+)`, 'gi'),
    (_value, name: string, separator: string) => `${name}${separator}[redacted]`,
  )
  safe = safe.replace(/'(?:'|[^'])*'|"(?:"|[^"])*"/g, (literal, offset) => {
    const around = `${safe.slice(Math.max(0, offset - 80), offset)} ${safe.slice(offset + literal.length, offset + literal.length + 80)}`
    return SECRET_NAME.test(around) ? quoted('[redacted]') : literal
  })
  return safe
}


const KNOWN_PHRASES: Array<[RegExp, string]> = [
  [/user with this email already exists/i, '该邮箱已注册。'],
  [/user with this username already exists/i, '该用户名已注册。'],
  [/invalid or expired (?:otp|code|verification code)/i, '验证码无效或已过期。'],
  [/no data rows returned by the query/i, catalog.viz.noData],
  [/document with the same unique key already exists/i, '记录已存在，不能重复添加。'],
  [/duplicate key/i, '记录已存在，不能重复添加。'],
  [/socket hang up/i, UNKNOWN_ERROR_SUMMARY],
]

function containsHan(text: string): boolean {
  return /[\u4e00-\u9fff]/.test(text)
}

function needsSafeSummary(text: string): boolean {
  if (KNOWN_PHRASES.some(([pattern]) => pattern.test(text))) return true
  if (!/[A-Za-z]{2,}/.test(text)) return false
  const latin = text.replace(/[\u4e00-\u9fff]/g, ' ')
  return /\b(?:DB|SQL|[A-Za-z]{4,})\b/i.test(latin)
}

function redactSensitiveText(text: string): string {
  return redactSecretLiterals(redactStructuredText(text)).replace(/\s+/g, ' ').trim()
}

export function sanitizeTechnicalDetail(text: string): string {
  const safe = redactSensitiveText(text)
  if (!safe || safe === '[redacted]') return ''
  return safe.slice(0, 500)
}



function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' ? (value as Record<string, unknown>) : undefined
}

function nestedData(record: Record<string, unknown>): Record<string, unknown> | undefined {
  const data = asRecord(record.data)
  if (data) return data
  const response = asRecord(record.response)
  return response ? asRecord(response.data) : undefined
}

function pickCode(value: unknown): string | undefined {
  const record = asRecord(value)
  if (!record) return undefined
  const nested = nestedData(record)
  const code = record.code ?? record.error_code ?? nested?.code ?? nested?.error_code
  return typeof code === 'string' || typeof code === 'number' ? String(code) : undefined
}

function readText(record: Record<string, unknown>, key: string): string | undefined {
  const nested = nestedData(record)
  const value = record[key] ?? nested?.[key]
  return typeof value === 'string' && value.trim() ? value : undefined
}

function pickHttp(value: unknown, explicit?: number): number | undefined {
  if (typeof explicit === 'number') return explicit
  const record = asRecord(value)
  if (!record) return undefined
  const response = asRecord(record.response)
  const status = record.status ?? record.statusCode ?? response?.status
  return typeof status === 'number' ? status : undefined
}

function withDetail(summary: string, detail: string | undefined, code?: string): ServiceErrorResult {
  return {
    summary,
    ...(detail ? { detail } : {}),
    ...(code ? { code } : {}),
  }
}

export function formatServiceError(error?: unknown, httpStatus?: number): ServiceErrorResult {
  const record = asRecord(error)
  if (
    record
    && typeof record.summary === 'string'
    && !readText(record, 'error')
    && !readText(record, 'message')
    && !readText(record, 'details')
  ) {
    const detail = typeof record.detail === 'string' ? sanitizeTechnicalDetail(record.detail) || undefined : undefined
    const code = typeof record.code === 'string' ? record.code : undefined
    return withDetail(record.summary, detail, code)
  }

  const code = pickCode(error)
  const message = typeof error === 'string'
    ? error
    : record
      ? readText(record, 'error') ?? readText(record, 'details') ?? readText(record, 'message')
      : undefined

  const details = record ? readText(record, 'details') : undefined
  const status = pickHttp(error, httpStatus)
  const mappedMessage = message
    ? zhCN.errors.messages[message] ?? KNOWN_PHRASES.find(([pattern]) => pattern.test(message))?.[1]
    : undefined
  const joined = [message, details && details !== message ? details : undefined]
    .filter((part): part is string => Boolean(part && part.trim()))
    .join('\n')
  const detail = joined ? sanitizeTechnicalDetail(joined) || undefined : undefined


  if (code && zhCN.errors.codes[code as keyof typeof zhCN.errors.codes]) {
    return withDetail(zhCN.errors.codes[code as keyof typeof zhCN.errors.codes], detail, code)
  }

  if (code && /^\d{3}$/.test(code) && zhCN.errors.http[Number(code)]) {
    return withDetail(zhCN.errors.http[Number(code)], detail, code)
  }

  if (mappedMessage) {
    return withDetail(mappedMessage, detail, code)
  }

  if (status && zhCN.errors.http[status]) {
    return withDetail(zhCN.errors.http[status], detail, code)
  }

  if (message && SSE_EXACT[message.trim()]) {
    return withDetail(SSE_EXACT[message.trim()], detail, code)
  }

  if (message && !needsSafeSummary(message) && containsHan(message)) {
    const visible = sanitizeTechnicalDetail(message)

    return withDetail(visible || UNKNOWN_ERROR_SUMMARY, detail, code)
  }

  if (message || details) {
    return withDetail(UNKNOWN_ERROR_SUMMARY, detail, code)
  }

  return { summary: UNKNOWN_ERROR_SUMMARY, ...(code ? { code } : {}) }
}

const SSE_EXACT: Record<string, string> = {
  'Fetching relevant data points & structure for the request..': catalog.sse.fetchingData,
  'Searching knowledge base for relevant context..': catalog.sse.searchingKb,
  'Setting up knowledge base for future queries (first-time setup)...': catalog.sse.settingUpKb,
  'Exploring knowledge base using tool-calling to discover relevant context..': catalog.sse.exploringKb,
  'Retrieving relevant earlier conversation context..': catalog.sse.retrievingContext,
  'Running the workflow & fetching the results..': catalog.sse.runningWorkflow,
  'Analyzing the criticality of the request..': catalog.sse.analyzingCriticality,
  'Analyzing the error to provide a clear explanation..': catalog.sse.analyzingError,
  'Query faced error, applying fix and retrying..': catalog.sse.applyingFix,
  'Query fix applied successfully!': catalog.sse.fixApplied,
  'Combining & structuring the response': catalog.sse.combining,
  'Generating visualization for the result': catalog.sse.generatingViz,
  'Operation cancelled by user': '已取消。',
  'Analyzing your database schema...': '正在分析数据库结构…',
  'Planning dashboard blueprints...': '正在规划仪表盘方案…',
  'Preparing blueprint suggestions...': '正在准备方案建议…',
  'Curating & testing the widget queries. This may take a few moments...': '正在整理并测试组件查询，可能需要一点时间…',
  'Regenerating dashboard...': '正在重新生成仪表盘…',
  'Finalizing regenerated dashboard...': '正在完成重新生成的仪表盘…',
  'Testing widget queries. This may take a few moments...': '正在测试组件查询，可能需要一点时间…',
  'Preparing your response...': '正在准备回复…',
  'The AI model was unable to generate a complete response. Please try again or use a different model.': '模型未能生成完整回复，请重试或更换模型。',
  'Error: Failed to complete the request, seems like the database connection issue, try reconnecting the database.': '请求未完成，可能是数据库连接问题，请重新连接。',
}

const SSE_PATTERNS: Array<[RegExp, (...groups: string[]) => string]> = [
  [/^Found (\d+) relevant tables from knowledge base$/, count => catalog.sse.foundTables(Number(count))],
  [/^Found (\d+) relevant earlier messages for context$/, count => catalog.sse.foundMessages(Number(count))],
  [/^Tool (.+) returned an error, retrying\.\.$/, name => catalog.sse.toolError(name)],
  [/^Exploring: (.+)$/, subject => `正在查看：${redactSensitiveText(subject) || '相关数据'}`],
  [/^Exploring database: calling (.+)$/, name => `正在查看数据库：${name}`],
  [/^Building dashboard (\d+)\/(\d+): (.+)\.\.\.$/, (current, total, name) => `正在构建仪表盘 ${current}/${total}：${name}`],
  [/^Finalizing dashboard: (.+)$/, name => `正在完成仪表盘：${name}`],
  [/^Failed to generate dashboard '(.+)': ([\s\S]+)$/, (_name, reason) => {
    const safe = redactSensitiveText(reason)
    return safe ? `生成仪表盘失败。\n${safe}` : '生成仪表盘失败。'
  }],
]

export function translateSseStatus(text: string): string {
  if (!text) return text
  const trimmed = text.trim()
  if (/analyzing your request/i.test(trimmed)) return zhCN.chat.analyzing
  if (SSE_EXACT[trimmed]) return SSE_EXACT[trimmed]
  for (const [pattern, render] of SSE_PATTERNS) {
    const match = trimmed.match(pattern)
    if (match) return render(...match.slice(1))
  }
  if (/^failed to generate dashboard\b/i.test(trimmed)) return '生成仪表盘失败。'
  const safe = redactSensitiveText(trimmed)
  if (containsHan(trimmed)) return safe || trimmed
  if (/^error:/i.test(trimmed) || needsSafeSummary(trimmed)) {
    return formatServiceError(trimmed).summary
  }
  return safe || trimmed
}


export function formatRelativeTime(dateString: string): string {
  const date = new Date(dateString)
  const now = new Date()
  const diffMinutes = Math.floor((now.getTime() - date.getTime()) / 60000)
  if (Number.isNaN(diffMinutes) || diffMinutes < 1) return zhCN.dates.justNow
  if (diffMinutes < 60) return `${diffMinutes} ${zhCN.dates.minutesAgo}`
  const diffHours = Math.floor(diffMinutes / 60)
  if (diffHours < 24) return `${diffHours} ${zhCN.dates.hoursAgo}`
  const diffDays = Math.floor(diffHours / 24)
  if (diffDays === 1) return zhCN.dates.yesterday
  return formatDateTime(dateString)
}

export function formatDateTime(dateString: string): string {
  const date = new Date(dateString)
  if (Number.isNaN(date.getTime())) return dateString
  return date.toLocaleString('zh-CN', {
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export function formatDateDivider(dateString: string): string {
  const date = new Date(dateString)
  const today = new Date()
  const yesterday = new Date(today)
  yesterday.setDate(yesterday.getDate() - 1)
  if (date.toDateString() === today.toDateString()) return zhCN.dates.today
  if (date.toDateString() === yesterday.toDateString()) return zhCN.dates.yesterday
  return date.toLocaleDateString('zh-CN', { year: 'numeric', month: 'long', day: 'numeric' })
}

export function dateGroupLabel(dateString: string): string {
  const date = new Date(dateString)
  const now = new Date()
  const diffDays = Math.floor((now.getTime() - date.getTime()) / 86400000)
  if (diffDays === 0) return zhCN.dates.today
  if (diffDays < 7) return zhCN.dates.days7
  if (diffDays < 30) return zhCN.dates.days30
  return date.toLocaleDateString('zh-CN', { year: 'numeric', month: 'long' })
}
