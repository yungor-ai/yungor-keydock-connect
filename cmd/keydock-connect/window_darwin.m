//go:build darwin && arm64 && cgo

#import <AppKit/AppKit.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
#include <stdlib.h>
#include "_cgo_export.h"
#include "window_darwin.h"

static NSString *const reopenNotification = @"ai.yungor.keydock-connect.reopen";

@interface KDWindowController : NSObject <NSApplicationDelegate, NSWindowDelegate>
@property NSWindow *window;
@property NSStatusItem *statusItem;
@property NSTextField *server;
@property NSSecureTextField *deviceKey;
@property NSTextField *device;
@property NSTextField *pairingStatus;
@property NSTextField *connectionNotice;
@property NSTextField *proxyTitle;
@property NSTextField *proxyStatus;
@property NSTextField *proxyUpdated;
@property NSTextField *proxyNotice;
@property NSTextField *diagnosticsNotice;
@property NSTextField *toolsNotice;
@property NSTextField *operationNotice;
@property NSTextField *hint;
@property NSView *troubleshooting;
@property NSArray<NSTextField *> *checkStatus;
@property NSArray<NSTextField *> *checkDetails;
@property NSButton *connectButton;
@property NSButton *softwareButton;
@property NSButton *updateButton;
@property NSButton *networkCheckButton;
@property NSPopUpButton *proxyModePicker;
@property NSButton *proxyButton;
@property NSMenuItem *restoreItem;
@property NSButton *exportButton;
@property int proxyMode;
@property BOOL busy;
@property BOOL quitting;
@property BOOL installingUpdate;
@property BOOL restorable;
@property BOOL restoring;
@property BOOL paired;
@property BOOL subscription;
@property BOOL networkChecking;
@end

static KDWindowController *ui;
static NSString *str(const char *buffer) {
    return buffer ? ([NSString stringWithUTF8String:buffer] ?: @"") : @"";
}

static NSTextField *label(NSString *text, CGFloat size, NSFontWeight weight, NSColor *color) {
    NSTextField *field = [NSTextField labelWithString:text];
    field.font = [NSFont systemFontOfSize:size weight:weight];
    field.textColor = color;
    return field;
}

static void singleLine(NSTextField *field) {
    field.lineBreakMode = NSLineBreakByTruncatingTail;
    [field setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow
                                    forOrientation:NSLayoutConstraintOrientationHorizontal];
}

static NSTextField *wrappedLabel(NSString *text, CGFloat size, NSColor *color) {
    NSTextField *field = [NSTextField wrappingLabelWithString:text];
    field.font = [NSFont systemFontOfSize:size];
    field.textColor = color;
    [field setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow
                                    forOrientation:NSLayoutConstraintOrientationHorizontal];
    return field;
}

static void setText(NSTextField *field, NSString *text) {
    field.stringValue = text;
    field.toolTip = text.length ? text : nil;
}

static NSStackView *stack(NSArray<NSView *> *views, NSUserInterfaceLayoutOrientation orientation, CGFloat spacing) {
    NSStackView *result = [NSStackView stackViewWithViews:views];
    result.orientation = orientation;
    result.alignment = orientation == NSUserInterfaceLayoutOrientationVertical ? NSLayoutAttributeLeading : NSLayoutAttributeCenterY;
    result.spacing = spacing;
    return result;
}

static NSColor *appearanceColor(NSUInteger light, NSUInteger dark, NSColor *accessible) {
    return [NSColor colorWithName:nil dynamicProvider:^NSColor *(NSAppearance *appearance) {
        if ([appearance.name containsString:@"HighContrast"]) return accessible;
        NSString *style = [appearance bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
        NSUInteger rgb = [style isEqualToString:NSAppearanceNameDarkAqua] ? dark : light;
        return [NSColor colorWithSRGBRed:((rgb >> 16) & 0xff) / 255.0 green:((rgb >> 8) & 0xff) / 255.0
                                  blue:(rgb & 0xff) / 255.0 alpha:1];
    }];
}

static NSColor *ink(void) { return appearanceColor(0x193137, 0xe6f1f2, NSColor.labelColor); }
static NSColor *muted(void) { return appearanceColor(0x4b626c, 0xb6cacf, NSColor.secondaryLabelColor); }
static NSColor *accent(void) { return appearanceColor(0x00666f, 0x71d2d5, NSColor.labelColor); }
static NSColor *success(void) { return appearanceColor(0x0f664d, 0x82d9b0, NSColor.systemGreenColor); }
static NSColor *danger(void) { return appearanceColor(0xa83034, 0xff9599, NSColor.systemRedColor); }

@interface KDSurface : NSView
@property NSColor *fill;
@property CGFloat cornerRadius;
@end

@implementation KDSurface
- (BOOL)isFlipped { return YES; }
- (void)drawRect:(NSRect)dirty {
    [self.fill setFill];
    if (self.cornerRadius > 0) {
        [[NSBezierPath bezierPathWithRoundedRect:self.bounds xRadius:self.cornerRadius yRadius:self.cornerRadius] fill];
    } else {
        NSRectFill(NSIntersectionRect(dirty, self.bounds));
    }
}
- (void)viewDidChangeEffectiveAppearance {
    [super viewDidChangeEffectiveAppearance];
    self.needsDisplay = YES;
}
@end

static void pin(NSView *view, NSView *parent, NSEdgeInsets insets) {
    view.translatesAutoresizingMaskIntoConstraints = NO;
    [parent addSubview:view];
    [NSLayoutConstraint activateConstraints:@[
        [view.leadingAnchor constraintEqualToAnchor:parent.leadingAnchor constant:insets.left],
        [view.trailingAnchor constraintEqualToAnchor:parent.trailingAnchor constant:-insets.right],
        [view.topAnchor constraintEqualToAnchor:parent.topAnchor constant:insets.top],
        [view.bottomAnchor constraintEqualToAnchor:parent.bottomAnchor constant:-insets.bottom]
    ]];
}

static KDSurface *surface(NSView *content, NSColor *color, NSEdgeInsets insets) {
    KDSurface *view = [KDSurface new];
    view.fill = color;
    if (content) pin(content, view, insets);
    return view;
}

static NSStackView *column(NSArray<NSView *> *views, CGFloat spacing) {
    NSStackView *result = stack(views, NSUserInterfaceLayoutOrientationVertical, spacing);
    result.detachesHiddenViews = NO;
    for (NSView *view in views) {
        [view.widthAnchor constraintEqualToAnchor:result.widthAnchor].active = YES;
    }
    return result;
}

static NSTextField *heading(NSString *text) { return label(text, 16, NSFontWeightSemibold, ink()); }

static NSGridView *rows(NSArray<NSView *> *views, CGFloat spacing) {
    NSMutableArray<NSArray<NSView *> *> *cells = [NSMutableArray new];
    for (NSView *view in views) [cells addObject:@[view]];
    NSGridView *result = [NSGridView gridViewWithViews:cells];
    [result columnAtIndex:0].xPlacement = NSGridCellPlacementFill;
    result.rowAlignment = NSGridRowAlignmentNone;
    result.rowSpacing = spacing;
    return result;
}

static void showRow(NSView *view, BOOL show) {
    NSGridView *grid = (NSGridView *)view.superview;
    [grid cellForView:view].row.hidden = !show;
}

static NSArray<NSString *> *checkDescriptions(void) {
    return @[@"从服务器获取设备配置", @"写入本机 Codex 配置", @"发送模型测试请求",
             @"测试设备分配的 SS 节点", @"检测 www.google.com", @"比较当前出口与 KeyDock IPv4"];
}

static NSGridView *checkGrid(NSArray<NSArray<NSView *> *> *checks, NSUInteger start, NSUInteger end) {
    NSGridView *grid = [NSGridView gridViewWithViews:[checks subarrayWithRange:NSMakeRange(start, end - start)]];
    grid.columnSpacing = 10;
    grid.rowSpacing = 4;
    [grid columnAtIndex:0].width = 86;
    [grid columnAtIndex:1].width = 60;
    [grid columnAtIndex:2].xPlacement = NSGridCellPlacementFill;
    grid.rowAlignment = NSGridRowAlignmentNone;
    grid.yPlacement = NSGridCellPlacementCenter;
    return grid;
}

@implementation KDWindowController

- (NSButton *)button:(NSString *)title action:(SEL)action {
    NSButton *button = [NSButton buttonWithTitle:title target:self action:action];
    button.bezelStyle = NSBezelStyleRounded;
    return button;
}

- (void)installMenu {
    NSMenu *bar = [NSMenu new];
    NSMenuItem *appItem = [NSMenuItem new];
    [bar addItem:appItem];
    appItem.submenu = [[NSMenu alloc] initWithTitle:@"KeyDock Connect"];
    [appItem.submenu addItemWithTitle:@"退出 KeyDock Connect" action:@selector(terminate:) keyEquivalent:@"q"];

    NSMenuItem *editItem = [NSMenuItem new];
    [bar addItem:editItem];
    editItem.submenu = [[NSMenu alloc] initWithTitle:@"编辑"];
    [editItem.submenu addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
    [editItem.submenu addItemWithTitle:@"复制" action:@selector(copy:) keyEquivalent:@"c"];
    [editItem.submenu addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
    [editItem.submenu addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];
    NSApp.mainMenu = bar;

    self.statusItem = [NSStatusBar.systemStatusBar statusItemWithLength:NSVariableStatusItemLength];
    NSStatusBarButton *statusButton = self.statusItem.button;
    NSImage *keyIcon = [NSImage imageWithSystemSymbolName:@"key.fill" accessibilityDescription:@"KeyDock Connect"];
    if (keyIcon) {
        keyIcon.template = YES;
        statusButton.image = keyIcon;
        statusButton.imagePosition = NSImageOnly;
    } else {
        statusButton.title = @"KeyDock";
    }
    statusButton.toolTip = @"KeyDock Connect";
    statusButton.accessibilityLabel = @"KeyDock Connect";

    NSMenu *statusMenu = [NSMenu new];
    NSMenuItem *show = [statusMenu addItemWithTitle:@"打开 KeyDock Connect"
                                            action:@selector(openFromStatusItem:) keyEquivalent:@""];
    show.target = self;
    [statusMenu addItem:NSMenuItem.separatorItem];
    NSMenuItem *quit = [statusMenu addItemWithTitle:@"退出接入助手" action:@selector(terminate:) keyEquivalent:@""];
    quit.target = NSApp;
    self.statusItem.menu = statusMenu;
}

- (void)openFromStatusItem:(NSMenuItem *)sender {
    [self reveal];
}

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    [self installMenu];
    self.window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 700, 700)
                                              styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                                                        NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
                                                backing:NSBackingStoreBuffered defer:NO];
    NSString *version = [NSBundle.mainBundle objectForInfoDictionaryKey:@"CFBundleShortVersionString"];
    self.window.title = version.length ? [@"KeyDock Connect v" stringByAppendingString:version] : @"KeyDock Connect";
    self.window.contentMinSize = NSMakeSize(540, 490);
    self.window.delegate = self;
    self.window.releasedWhenClosed = NO;

    NSColor *primary = ink(), *secondary = muted();
    NSColor *page = appearanceColor(0xf0f4f5, 0x192124, NSColor.windowBackgroundColor);
    NSColor *card = appearanceColor(0xffffff, 0x252e31, NSColor.controlBackgroundColor);
    NSColor *teal = appearanceColor(0xdef1ef, 0x21403e, NSColor.controlBackgroundColor);
    NSColor *proxyFill = appearanceColor(0xe5f1f4, 0x233c41, NSColor.controlBackgroundColor);
    NSColor *badge = appearanceColor(0xf0f5f6, 0x314247, NSColor.windowBackgroundColor);
    NSColor *warning = appearanceColor(0xfff6e0, 0x3e3625, NSColor.controlBackgroundColor);

    NSImage *appIcon = [NSImage imageNamed:NSImageNameApplicationIcon];
    NSImageView *icon = [NSImageView imageViewWithImage:appIcon];
    icon.imageScaling = NSImageScaleProportionallyUpOrDown;
    [icon setAccessibilityElement:NO];
    [icon.widthAnchor constraintEqualToConstant:44].active = YES;
    [icon.heightAnchor constraintEqualToConstant:44].active = YES;
    NSTextField *title = label(@"KeyDock Connect", 23, NSFontWeightSemibold, primary);
    self.pairingStatus = label(@"正在读取设备授权…", 13, NSFontWeightMedium, secondary);
    KDSurface *header = surface(stack(@[icon, title, [NSView new], self.pairingStatus],
                                     NSUserInterfaceLayoutOrientationHorizontal, 12), teal, NSEdgeInsetsMake(10, 22, 10, 22));

    self.server = [NSTextField textFieldWithString:@""];
    self.server.placeholderString = @"https://keydock.example.com";
    self.server.bezelStyle = NSTextFieldSquareBezel;
    self.server.accessibilityLabel = @"服务器地址";
    self.server.accessibilityIdentifier = @"keydock.server";
    self.deviceKey = [NSSecureTextField new];
    self.deviceKey.placeholderString = @"输入设备密钥";
    self.deviceKey.bezelStyle = NSTextFieldSquareBezel;
    self.deviceKey.accessibilityLabel = @"设备密钥（Device Key）";
    for (NSTextField *input in @[self.server, self.deviceKey]) {
        input.font = [NSFont systemFontOfSize:13];
        [input setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
        [input.heightAnchor constraintEqualToConstant:32].active = YES;
    }
    self.connectButton = [self button:@"连接并测试" action:@selector(connect:)];
    self.connectButton.keyEquivalent = @"\r";
    self.window.defaultButtonCell = self.connectButton.cell;
    [self.connectButton.widthAnchor constraintEqualToConstant:110].active = YES;
    NSStackView *serverField = column(@[label(@"服务器地址", 13, NSFontWeightRegular, secondary), self.server], 4);
    NSStackView *keyField = column(@[label(@"设备密钥（Device Key）", 13, NSFontWeightRegular, secondary), self.deviceKey], 4);
    NSStackView *form = stack(@[serverField, keyField, self.connectButton], NSUserInterfaceLayoutOrientationHorizontal, 10);
    form.distribution = NSStackViewDistributionFill;
    form.alignment = NSLayoutAttributeBottom;
    [serverField.widthAnchor constraintEqualToAnchor:keyField.widthAnchor].active = YES;
    self.server.nextKeyView = self.deviceKey;
    self.deviceKey.nextKeyView = self.connectButton;
    self.softwareButton = [self button:@"软件下载" action:@selector(openSoftware:)];
    self.softwareButton.toolTip = @"在浏览器打开此服务器的软件下载页面";
    self.softwareButton.accessibilityIdentifier = @"keydock.software";
    self.updateButton = [self button:@"检查更新" action:@selector(checkUpdate:)];
    self.updateButton.accessibilityIdentifier = @"keydock.check-update";
    self.device = label(@"请输入管理员提供的服务器地址和设备密钥。", 13, NSFontWeightRegular, secondary);
    singleLine(self.device);
    self.connectionNotice = wrappedLabel(@"", 13, secondary);
    self.connectionNotice.accessibilityIdentifier = @"keydock.connection-notice";
    NSEdgeInsets sectionInsets = NSEdgeInsetsMake(13, 18, 14, 18);
    KDSurface *connection = surface(rows(@[heading(@"设备接入"), self.device, form, self.connectionNotice], 7), card, sectionInsets);
    connection.cornerRadius = 10;
    showRow(self.connectionNotice, NO);

    self.exportButton = [self button:@"导出诊断日志" action:@selector(exportLog:)];
    self.exportButton.toolTip = @"保存连接诊断结果，便于交给管理员排查";
    NSStackView *checkHeader = stack(@[heading(@"连接检查"), [NSView new], self.exportButton],
                                     NSUserInterfaceLayoutOrientationHorizontal, 8);
    NSMutableArray<NSArray<NSView *> *> *results = [NSMutableArray new];
    NSMutableArray<NSTextField *> *statuses = [NSMutableArray new];
    NSMutableArray<NSTextField *> *details = [NSMutableArray new];
    NSArray<NSString *> *stages = @[@"设备授权", @"Codex 配置", @"模型连通", @"代理连通", @"Google 连通", @"出口 IPv4"];
    NSArray<NSString *> *descriptions = checkDescriptions();
    for (NSUInteger row = 0; row < stages.count; row++) {
        NSTextField *status = label(@"待检查", 12, NSFontWeightMedium, secondary);
        status.alignment = NSTextAlignmentCenter;
        KDSurface *statusBadge = surface(status, badge, NSEdgeInsetsMake(3, 0, 3, 0));
        [statusBadge.widthAnchor constraintEqualToConstant:60].active = YES;
        NSTextField *detail = wrappedLabel(descriptions[row], 13, secondary);
        [statuses addObject:status];
        [details addObject:detail];
        [results addObject:@[label(stages[row], 13, NSFontWeightRegular, primary), statusBadge, detail]];
    }
    self.checkStatus = statuses;
    self.checkDetails = details;
    NSTextField *accessHeading = label(@"设备接入检测", 13, NSFontWeightSemibold, primary);
    self.hint = wrappedLabel(@"", 13, primary);
    NSTextField *troubleshootingTitle = label(@"代理故障排查", 13, NSFontWeightSemibold, primary);
    KDSurface *troubleshooting = surface(column(@[troubleshootingTitle, self.hint], 4), warning, NSEdgeInsetsMake(7, 10, 8, 10));
    troubleshooting.cornerRadius = 6;
    self.troubleshooting = troubleshooting;
    self.networkCheckButton = [self button:@"重新检测网络" action:@selector(recheckNetwork:)];
    self.networkCheckButton.toolTip = @"通过当前网络重新检测 Google 和出口 IPv4";
    NSTextField *networkHeading = label(@"网络访问检测", 13, NSFontWeightSemibold, primary);
    NSTextField *networkProtocol = label(@"TCP HTTPS", 11, NSFontWeightMedium, secondary);
    NSStackView *networkHeader = stack(@[networkHeading, networkProtocol, [NSView new], self.networkCheckButton],
                                      NSUserInterfaceLayoutOrientationHorizontal, 8);
    KDSurface *divider = surface(nil, badge, NSEdgeInsetsMake(0, 0, 0, 0));
    [divider.heightAnchor constraintEqualToConstant:1].active = YES;
    self.diagnosticsNotice = wrappedLabel(@"", 13, secondary);
    self.diagnosticsNotice.accessibilityIdentifier = @"keydock.diagnostics-notice";
    KDSurface *checkCard = surface(rows(@[checkHeader, self.diagnosticsNotice, accessHeading, checkGrid(results, KD_CHECK_DEVICE, KD_CHECK_GOOGLE),
                                         troubleshooting, divider, networkHeader, checkGrid(results, KD_CHECK_GOOGLE, stages.count)], 7),
                                  card, sectionInsets);
    checkCard.cornerRadius = 10;
    showRow(self.diagnosticsNotice, NO);
    showRow(troubleshooting, NO);

    self.proxyTitle = label(@"未启用", 13, NSFontWeightSemibold, secondary);
    NSStackView *proxyHeader = stack(@[heading(@"网络代理"), [NSView new], self.proxyTitle],
                                     NSUserInterfaceLayoutOrientationHorizontal, 8);
    self.proxyModePicker = [[NSPopUpButton alloc] initWithFrame:NSZeroRect pullsDown:NO];
    [self.proxyModePicker addItemsWithTitles:@[@"系统代理", @"TUN（虚拟网卡）"]];
    [self.proxyModePicker itemAtIndex:0].tag = KD_PROXY_SYSTEM;
    [self.proxyModePicker itemAtIndex:1].tag = KD_PROXY_TUN;
    self.proxyModePicker.accessibilityLabel = @"代理模式";
    self.proxyModePicker.target = self;
    self.proxyModePicker.action = @selector(proxyModeChanged:);
    [self.proxyModePicker.widthAnchor constraintGreaterThanOrEqualToConstant:126].active = YES;
    self.proxyButton = [self button:@"启用系统代理" action:@selector(toggleProxy:)];
    self.proxyStatus = wrappedLabel(@"", 13, primary);
    self.proxyUpdated = wrappedLabel(@"", 12, secondary);
    self.proxyNotice = wrappedLabel(@"", 13, secondary);
    self.proxyNotice.accessibilityIdentifier = @"keydock.proxy-notice";
    NSStackView *proxyControls = stack(@[label(@"代理模式", 13, NSFontWeightRegular, secondary), self.proxyModePicker,
                                        [NSView new], self.proxyButton], NSUserInterfaceLayoutOrientationHorizontal, 8);
    KDSurface *proxyCard = surface(rows(@[proxyHeader, proxyControls, self.proxyStatus,
                                         self.proxyUpdated, self.proxyNotice], 5), proxyFill, sectionInsets);
    proxyCard.cornerRadius = 10;
    showRow(self.proxyUpdated, NO);
    showRow(self.proxyNotice, NO);

    NSStackView *body = column(@[connection, proxyCard, checkCard], 10);
    KDSurface *document = surface(body, page, NSEdgeInsetsMake(12, 16, 12, 16));
    NSScrollView *scroll = [NSScrollView new];
    scroll.drawsBackground = NO;
    scroll.hasVerticalScroller = YES;
    scroll.autohidesScrollers = YES;
    scroll.scrollerStyle = NSScrollerStyleOverlay;
    scroll.documentView = document;
    document.translatesAutoresizingMaskIntoConstraints = NO;
    [NSLayoutConstraint activateConstraints:@[
        [document.topAnchor constraintEqualToAnchor:scroll.contentView.topAnchor],
        [document.leadingAnchor constraintEqualToAnchor:scroll.contentView.leadingAnchor],
        [document.widthAnchor constraintEqualToAnchor:scroll.contentView.widthAnchor]
    ]];

    self.toolsNotice = wrappedLabel(@"", 13, secondary);
    self.toolsNotice.maximumNumberOfLines = 3;
    self.toolsNotice.lineBreakMode = NSLineBreakByTruncatingTail;
    self.toolsNotice.accessibilityIdentifier = @"keydock.notice";
    NSPopUpButton *more = [[NSPopUpButton alloc] initWithFrame:NSZeroRect pullsDown:YES];
    [more addItemWithTitle:@"更多操作"];
    more.accessibilityLabel = @"更多操作";
    more.menu.autoenablesItems = NO;
    self.restoreItem = [more.menu addItemWithTitle:@"恢复原 Codex 配置" action:@selector(restore:) keyEquivalent:@""];
    self.restoreItem.target = self;
    NSMenuItem *quit = [more.menu addItemWithTitle:@"退出接入助手" action:@selector(terminate:) keyEquivalent:@""];
    quit.target = NSApp;
    NSTextField *menuBarHint = label(@"关闭窗口后可从菜单栏打开", 12, NSFontWeightRegular, secondary);
    singleLine(menuBarHint);
    NSStackView *tools = stack(@[self.softwareButton, self.updateButton, [NSView new], menuBarHint, more],
                               NSUserInterfaceLayoutOrientationHorizontal, 8);
    KDSurface *footer = surface(rows(@[self.toolsNotice, tools], 5), card, NSEdgeInsetsMake(7, 20, 7, 16));
    showRow(self.toolsNotice, NO);

    KDSurface *root = surface(nil, page, NSEdgeInsetsMake(0, 0, 0, 0));
    self.window.contentView = root;
    for (NSView *view in @[header, scroll, footer]) {
        view.translatesAutoresizingMaskIntoConstraints = NO;
        [root addSubview:view];
        [view.leadingAnchor constraintEqualToAnchor:root.leadingAnchor].active = YES;
        [view.trailingAnchor constraintEqualToAnchor:root.trailingAnchor].active = YES;
    }
    [NSLayoutConstraint activateConstraints:@[
        [header.topAnchor constraintEqualToAnchor:root.topAnchor],
        [scroll.topAnchor constraintEqualToAnchor:header.bottomAnchor],
        [scroll.bottomAnchor constraintEqualToAnchor:footer.topAnchor],
        [footer.bottomAnchor constraintEqualToAnchor:root.bottomAnchor]
    ]];

    self.busy = YES;
    [self updateButtons];
    [self.window center];
    [self reveal];
    [[NSDistributedNotificationCenter defaultCenter] addObserver:self selector:@selector(reopen:)
                                                           name:reopenNotification object:nil
                                             suspensionBehavior:NSNotificationSuspensionBehaviorDeliverImmediately];
    [NSTimer scheduledTimerWithTimeInterval:3 target:self selector:@selector(pollProxy:) userInfo:nil repeats:YES];
    kdMacReady();
}

- (void)reveal {
    if (!self.quitting) {
        if (NSApp.activationPolicy != NSApplicationActivationPolicyRegular) {
            [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        }
        if (self.window.miniaturized) [self.window deminiaturize:nil];
        [self.window makeKeyAndOrderFront:nil];
        [NSApp activateIgnoringOtherApps:YES];
    }
}

- (BOOL)applicationShouldHandleReopen:(NSApplication *)sender hasVisibleWindows:(BOOL)visible {
    [self reveal];
    return YES;
}

- (void)reopen:(NSNotification *)notification {
    dispatch_async(dispatch_get_main_queue(), ^{ [self reveal]; });
}

- (BOOL)windowShouldClose:(NSWindow *)sender {
    [self.window makeFirstResponder:nil];
    self.deviceKey.stringValue = @"";
    [sender orderOut:nil];
    if (self.statusItem.button) [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    return NO;
}

- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender {
    if (!self.quitting) {
        self.quitting = YES;
        [self updateButtons];
        kdMacQuit(self.installingUpdate ? 1 : 0);
    }
    return NSTerminateLater;
}

- (void)updateButtons {
    BOOL idle = !self.busy && !self.quitting;
    self.connectButton.enabled = idle;
    self.softwareButton.enabled = idle;
    self.updateButton.enabled = idle;
    self.networkCheckButton.enabled = !self.networkChecking && !self.quitting;
    self.server.enabled = idle;
    self.deviceKey.enabled = idle;
    self.restoreItem.enabled = idle && self.restorable;
    self.exportButton.enabled = idle;
    [self renderProxy];
}

- (int)selectedProxyMode {
    return self.proxyModePicker.selectedTag == KD_PROXY_TUN ? KD_PROXY_TUN : KD_PROXY_SYSTEM;
}

- (void)renderProxy {
    int selected = [self selectedProxyMode];
    BOOL active = self.proxyMode != KD_PROXY_INACTIVE;
    BOOL idle = !self.busy && !self.quitting;
    self.proxyModePicker.enabled = idle;
    self.proxyButton.enabled = idle && (selected == self.proxyMode || self.subscription);

    NSString *title = @"未启用";
    if (active) title = self.proxyMode == KD_PROXY_TUN ? @"TUN 已启用" : @"系统代理已启用";
    NSString *action = !active ? @"启用" : (selected == self.proxyMode ? @"关闭" : @"切换至");
    NSString *detail;
    if (!self.subscription && !active) {
        detail = self.paired ? @"当前设备未分配代理订阅。需要使用代理时，请联系管理员。" :
                                @"请先完成设备接入。取得代理订阅后可在这里启用。";
    } else if (selected == KD_PROXY_TUN) {
        detail = self.proxyMode == KD_PROXY_TUN ? @"流量正在通过虚拟网卡按订阅规则分流。" :
                                                  @"TUN 通过虚拟网卡分流。首次启用时可能需要在 macOS 中批准后台项目。";
    } else {
        detail = self.proxyMode == KD_PROXY_SYSTEM ? @"使用 macOS 系统代理的应用已按订阅规则分流。" :
                                                     @"启用后，助手会自动设置 macOS 系统代理。";
    }
    self.proxyButton.title = [action stringByAppendingString:selected == KD_PROXY_TUN ? @" TUN" : @"系统代理"];
    self.proxyTitle.textColor = active ? success() : muted();
    setText(self.proxyTitle, title);
    setText(self.proxyStatus, detail);
}

- (void)updateProxyMode:(int)mode {
    self.proxyMode = mode;
    [self updateButtons];
}

- (void)pollProxy:(NSTimer *)timer {
    if (self.busy || self.quitting || self.proxyMode == KD_PROXY_INACTIVE) return;
    int mode = kdMacProxyMode();
    if (self.proxyMode == mode) return;
    int previousMode = self.proxyMode;
    [self updateProxyMode:mode];
    if (mode == KD_PROXY_INACTIVE) {
        NSString *notice = previousMode == KD_PROXY_TUN ? @"TUN 已停止。可重新启用或导出诊断日志。" :
                                                           @"系统代理已停止。可重新启用或导出诊断日志。";
        [self showNotice:notice failure:YES inField:self.proxyNotice];
    }
}

- (void)showNotice:(NSString *)message failure:(BOOL)failure inField:(NSTextField *)field {
    if (!field) return;
    setText(field, message);
    field.textColor = failure ? danger() : ink();
    showRow(field, message.length > 0);
}

- (void)showTroubleshooting:(NSString *)tip {
    setText(self.hint, tip);
    showRow(self.troubleshooting, tip.length > 0);
}

- (void)runAction:(int)action first:(NSString *)first second:(NSString *)second
           progress:(NSString *)progress inField:(NSTextField *)field {
    if (self.busy || self.quitting) return;
    self.busy = YES;
    self.operationNotice = field;
    self.restoring = action == KD_RESTORE;
    [self updateButtons];
    [self showNotice:progress failure:NO inField:field];
    kdMacAction(action, (char *)first.UTF8String, (char *)second.UTF8String);
}

- (void)resetChecksFrom:(NSUInteger)start through:(NSUInteger)end {
    NSArray<NSString *> *descriptions = checkDescriptions();
    for (NSUInteger row = start; row <= end; row++) {
        NSTextField *status = self.checkStatus[row];
        status.stringValue = @"待检查";
        status.textColor = muted();
        self.checkDetails[row].textColor = muted();
        setText(self.checkDetails[row], descriptions[row]);
    }
}

- (void)connect:(id)sender {
    if (self.busy || self.quitting) return;
    [self.window makeFirstResponder:nil];
    NSString *key = self.deviceKey.stringValue;
    self.deviceKey.stringValue = @"";
    [self resetChecksFrom:KD_CHECK_DEVICE through:KD_CHECK_PROXY];
    [self showTroubleshooting:@""];
    [self runAction:KD_CONNECT first:self.server.stringValue second:key
           progress:@"正在接入设备并测试连接…" inField:self.connectionNotice];
}

- (void)openSoftware:(id)sender {
    [self.window makeFirstResponder:nil];
    char *value = kdMacSoftwareURL((char *)self.server.stringValue.UTF8String);
    NSURL *url = value ? [NSURL URLWithString:str(value)] : nil;
    free(value);
    if (!url) {
        [self.window makeFirstResponder:self.server];
        [self showNotice:@"请填写有效的服务器地址，再打开软件下载。" failure:YES inField:self.connectionNotice];
    } else if (![NSWorkspace.sharedWorkspace openURL:url]) {
        [self showNotice:@"无法打开软件下载页面。" failure:YES inField:self.toolsNotice];
    }
}

- (void)checkUpdate:(id)sender {
    if (self.busy || self.quitting) return;
    [self.window makeFirstResponder:nil];
    self.busy = YES;
    self.operationNotice = self.toolsNotice;
    [self updateButtons];
    [self showNotice:@"正在检查更新…" failure:NO inField:self.toolsNotice];
    kdMacCheckUpdate((char *)self.server.stringValue.UTF8String);
}

- (void)recheckNetwork:(id)sender {
    self.networkChecking = YES;
    self.networkCheckButton.enabled = NO;
    kdMacRecheckNetwork();
}

- (void)proxyModeChanged:(id)sender {
    [self showNotice:@"" failure:NO inField:self.proxyNotice];
    [self renderProxy];
}

- (void)toggleProxy:(id)sender {
    int selected = [self selectedProxyMode];
    NSString *toggle = selected == self.proxyMode ? @"off" : @"on";
    NSString *mode = selected == KD_PROXY_TUN ? @"tun" : @"system";
    NSString *progress = [NSString stringWithFormat:@"正在%@…", self.proxyButton.title];
    [self runAction:KD_PROXY first:toggle second:mode progress:progress inField:self.proxyNotice];
}

- (void)restore:(id)sender {
    NSAlert *alert = [NSAlert new];
    alert.messageText = @"恢复 KeyDock 接入前的 Codex 设置？";
    alert.informativeText = @"将撤销接入助手管理的模型和提供商设置，并保留其他 Codex 设置。";
    [alert addButtonWithTitle:@"恢复配置"];
    [alert addButtonWithTitle:@"取消"].keyEquivalent = @"\e";
    [alert beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse response) {
        if (response == NSAlertFirstButtonReturn) {
            [self runAction:KD_RESTORE first:nil second:nil progress:@"正在恢复 Codex 配置…" inField:self.toolsNotice];
        }
    }];
}

- (void)exportLog:(id)sender {
    NSSavePanel *panel = [NSSavePanel savePanel];
    NSDateFormatter *formatter = [NSDateFormatter new];
    formatter.locale = [[NSLocale alloc] initWithLocaleIdentifier:@"en_US_POSIX"];
    formatter.dateFormat = @"yyyyMMdd-HHmmss";
    panel.nameFieldStringValue = [NSString stringWithFormat:@"KeyDockConnect-diagnostics-%@.json", [formatter stringFromDate:NSDate.date]];
    panel.allowedContentTypes = @[UTTypeJSON];
    panel.canCreateDirectories = YES;
    [panel beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse result) {
        if (result == NSModalResponseOK) {
            [self runAction:KD_EXPORT first:panel.URL.path second:nil progress:@"正在导出诊断日志…" inField:self.diagnosticsNotice];
        }
    }];
}
@end

void kdRunWindow(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        ui = [KDWindowController new];
        NSApp.delegate = ui;
        [NSApp run];
    }
}

void kdShowError(const char *message) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        [NSApp activateIgnoringOtherApps:YES];
        NSAlert *alert = [NSAlert new];
        alert.alertStyle = NSAlertStyleCritical;
        alert.messageText = @"KeyDock 接入助手";
        alert.informativeText = str(message);
        [alert runModal];
    }
}

void kdReopenWindow(void) {
    @autoreleasepool {
        [[NSDistributedNotificationCenter defaultCenter] postNotificationName:reopenNotification object:nil
                                                                       userInfo:nil deliverImmediately:YES];
    }
}

void kdSetState(const char *server, const char *device, const char *model,
                int paired, int restorable, int subscription, int proxyMode,
                const char *updatedAt, int refreshFailed) {
    @autoreleasepool {
        NSString *initialServer = server ? str(server) : nil;
        NSString *deviceName = str(device), *modelName = str(model), *updated = str(updatedAt);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (ui.quitting) return;
            if (initialServer) {
                ui.server.stringValue = initialServer;
                ui.busy = NO;
            }
            ui.paired = paired;
            ui.deviceKey.placeholderString = paired ? @"同一服务器可留空" : @"输入设备密钥";
            setText(ui.pairingStatus, paired ? @"已保存设备授权" : @"等待设备接入");
            ui.pairingStatus.textColor = paired ? success() : muted();
            if (deviceName.length && modelName.length) {
                setText(ui.device, [NSString stringWithFormat:@"设备 %@   ·   模型 %@", deviceName, modelName]);
            } else if (deviceName.length) {
                setText(ui.device, [@"设备 " stringByAppendingString:deviceName]);
            } else {
                setText(ui.device, @"请输入管理员提供的服务器地址和设备密钥。");
            }
            ui.restorable = restorable;
            ui.subscription = subscription;
            [ui updateProxyMode:proxyMode];
            if (initialServer) [ui.window makeFirstResponder:paired ? ui.connectButton : ui.server];
            NSString *updatedText = updated.length ? [@"订阅最近更新  " stringByAppendingString:updated] : @"订阅尚未刷新";
            if (refreshFailed) updatedText = [updatedText stringByAppendingString:@"   ·   最近一次刷新失败"];
            ui.proxyUpdated.textColor = refreshFailed ? danger() : muted();
            setText(ui.proxyUpdated, updatedText);
            showRow(ui.proxyUpdated, subscription);
        });
    }
}

void kdSetCheck(int row, const char *status, const char *message, const char *hint) {
    @autoreleasepool {
        NSString *outcome = str(status), *detail = str(message), *tip = str(hint);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (ui.quitting || row < 0 || (NSUInteger)row >= ui.checkStatus.count) return;
            NSTextField *field = ui.checkStatus[row];
            NSString *passed = @"完成", *failed = @"失败", *skipped = @"跳过";
            if (row == KD_CHECK_GOOGLE) {
                passed = @"可访问";
                failed = @"无法访问";
            } else if (row == KD_CHECK_EGRESS) {
                passed = @"一致";
                skipped = @"未校验";
            }
            BOOL mismatch = row == KD_CHECK_EGRESS && [outcome isEqualToString:@"warning"];
            if ([outcome isEqualToString:@"ok"]) {
                field.stringValue = passed;
                field.textColor = success();
            } else if (mismatch) {
                field.stringValue = @"不一致";
                field.textColor = danger();
            } else if ([outcome isEqualToString:@"failed"]) {
                field.stringValue = failed;
                field.textColor = danger();
            } else {
                BOOL running = [outcome isEqualToString:@"running"];
                field.stringValue = running ? @"进行中" : skipped;
                field.textColor = running ? accent() : muted();
            }
            NSTextField *detailField = ui.checkDetails[row];
            setText(detailField, detail);
            detailField.textColor = mismatch ? danger() : muted();
            if (row == KD_CHECK_PROXY) [ui showTroubleshooting:tip];
            if (row == KD_CHECK_GOOGLE && [outcome isEqualToString:@"running"]) {
                ui.networkChecking = YES;
                ui.networkCheckButton.enabled = NO;
            } else if (row == KD_CHECK_EGRESS && ![outcome isEqualToString:@"running"]) {
                ui.networkChecking = NO;
                ui.networkCheckButton.enabled = YES;
            }
        });
    }
}

void kdSetProxyApprovalProgress(int waiting) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (!ui.quitting && ui.busy) {
            NSString *message = waiting ? @"请在系统设置中允许 KeyDock Connect 在后台运行，批准后会自动继续。" :
                                          @"后台服务已获准，正在启用代理…";
            [ui showNotice:message failure:NO inField:ui.proxyNotice];
        }
    });
}

void kdFinishOperation(const char *message, int failed) {
    @autoreleasepool {
        NSString *notice = str(message);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (ui.quitting) return;
            ui.busy = NO;
            [ui updateButtons];
            [ui showNotice:notice failure:failed inField:ui.operationNotice];
            ui.operationNotice = nil;
            if (ui.restoring && !failed) {
                [ui resetChecksFrom:KD_CHECK_CODEX through:KD_CHECK_MODEL];
                [ui showNotice:@"已恢复原 Codex 配置。再次接入时请点击「连接并测试」。"
                        failure:NO inField:ui.connectionNotice];
            }
            ui.restoring = NO;
        });
    }
}

void kdOfferUpdate(const char *version) {
    @autoreleasepool {
        NSString *release = str(version);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (ui.quitting) return;
            [ui reveal];
            [ui showNotice:[NSString stringWithFormat:@"发现新版本 v%@。", release] failure:NO inField:ui.toolsNotice];
            NSAlert *alert = [NSAlert new];
            alert.messageText = [NSString stringWithFormat:@"是否更新到 v%@？", release];
            alert.informativeText = @"更新将下载安装包，停止接入助手正在运行的代理，然后重启接入助手。";
            [alert addButtonWithTitle:@"在线更新"];
            [alert addButtonWithTitle:@"取消"].keyEquivalent = @"\e";
            [alert beginSheetModalForWindow:ui.window completionHandler:^(NSModalResponse response) {
                if (ui.quitting) return;
                BOOL accepted = response == NSAlertFirstButtonReturn;
                kdMacUpdateDecision(accepted ? 1 : 0);
                if (accepted) {
                    [ui showNotice:@"正在下载更新…" failure:NO inField:ui.toolsNotice];
                } else {
                    ui.busy = NO;
                    [ui updateButtons];
                    ui.operationNotice = nil;
                    [ui showNotice:@"已取消更新。" failure:NO inField:ui.toolsNotice];
                }
            }];
        });
    }
}

void kdSetUpdateProgress(const char *message) {
    @autoreleasepool {
        NSString *notice = str(message);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (!ui.quitting) [ui showNotice:notice failure:NO inField:ui.toolsNotice];
        });
    }
}

void kdRequestUpdateTermination(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (ui.quitting) return;
        ui.installingUpdate = YES;
        [ui showNotice:@"正在安装更新…" failure:NO inField:ui.toolsNotice];
        // The NSTerminateLater loop must be able to run the reply queued by Go.
        [NSApp performSelector:@selector(terminate:) withObject:nil afterDelay:0];
    });
}

void kdFinishTermination(const char *failure) {
    @autoreleasepool {
        NSString *message = str(failure);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (!message.length) {
                [NSApp replyToApplicationShouldTerminate:YES];
                return;
            }
            kdMacResumeActions();
            ui.installingUpdate = NO;
            ui.quitting = NO;
            ui.busy = NO;
            ui.restoring = NO;
            [ui updateButtons];
            [ui showNotice:@"" failure:NO inField:ui.operationNotice];
            ui.operationNotice = nil;
            [ui showNotice:message failure:YES inField:ui.toolsNotice];
            [NSApp replyToApplicationShouldTerminate:NO];
            [ui reveal];
            kdMacRecheckNetwork();
        });
    }
}
