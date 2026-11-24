using System;
using System.Text;
using System.Drawing;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;

public static class KeyDockWindowProbe {
    private delegate bool EnumProc(IntPtr hwnd, IntPtr arg);
    [DllImport("user32.dll")] private static extern bool EnumWindows(EnumProc cb, IntPtr unused);
    [DllImport("user32.dll")] private static extern bool EnumChildWindows(IntPtr parent, EnumProc cb, IntPtr unused);
    [DllImport("user32.dll")] private static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint process);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] private static extern int GetClassName(IntPtr hwnd,StringBuilder str,int size);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] private static extern int GetWindowText(IntPtr hwnd,StringBuilder text,int size);
    [DllImport("user32.dll", CharSet=CharSet.Unicode, EntryPoint="SendMessageTimeoutW")] private static extern IntPtr ReadText(IntPtr hwnd,uint msg,IntPtr size,StringBuilder text,uint flags,uint ms,out IntPtr result);
    [DllImport("user32.dll", CharSet=CharSet.Unicode, EntryPoint="SendMessageTimeoutW")] private static extern IntPtr WriteText(IntPtr hwnd,uint msg,IntPtr size,string text,uint flags,uint ms,out IntPtr result);
    [DllImport("user32.dll", EntryPoint="SendMessageTimeoutW")] private static extern IntPtr Send(IntPtr hwnd,uint msg,IntPtr w,IntPtr l,uint flags,uint ms,out IntPtr result);
    private const uint TimedFlags=0x22; // SMTO_ABORTIFHUNG | SMTO_ERRORONEXIT
    [DllImport("user32.dll", EntryPoint="PostMessageW")] private static extern bool Post(IntPtr hwnd,uint msg,IntPtr w,IntPtr l);
    [DllImport("user32.dll", EntryPoint="GetWindowLongW")] private static extern int GetStyle(IntPtr hwnd,int index);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hwnd);
    [DllImport("user32.dll")] private static extern bool IsWindowEnabled(IntPtr hwnd);
    [DllImport("user32.dll")] private static extern bool GetWindowRect(IntPtr hwnd,out Rect rect);
    [DllImport("dwmapi.dll")] private static extern int DwmGetWindowAttribute(IntPtr window,int attr,out Rect value,int size);
    [DllImport("user32.dll")] private static extern bool GetClientRect(IntPtr window,out Rect value);
    [DllImport("user32.dll")] private static extern bool ClientToScreen(IntPtr window,ref Point pt);
    [DllImport("user32.dll")] private static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] private static extern IntPtr GetParent(IntPtr hwnd);
    [DllImport("user32.dll")] private static extern uint GetDpiForWindow(IntPtr hwnd);
    [DllImport("user32.dll")] private static extern IntPtr SetThreadDpiAwarenessContext(IntPtr context);
    [DllImport("user32.dll")] private static extern bool PrintWindow(IntPtr hwnd,IntPtr hdc,uint flags);
    [DllImport("user32.dll",CharSet=CharSet.Unicode)] private static extern int GetMenuString(IntPtr menu,uint pos,StringBuilder text,int len,uint byPosition);
    [DllImport("user32.dll")] private static extern int GetSystemMetrics(int index);
    [DllImport("shell32.dll")] private static extern int Shell_NotifyIconGetRect(ref TrayItem id,out Rect box);
    [DllImport("dwmapi.dll")] private static extern int DwmFlush();
    [DllImport("user32.dll")] private static extern bool GetGUIThreadInfo(uint thread,ref GuiInfo info);
    [DllImport("user32.dll",CharSet=CharSet.Unicode)] private static extern IntPtr FindWindowEx(IntPtr parent,IntPtr after,string cls,string caption);
    [DllImport("user32.dll")] private static extern bool AllowSetForegroundWindow(uint process);
    [DllImport("user32.dll")] private static extern IntPtr GetShellWindow();
    [DllImport("user32.dll")] private static extern IntPtr GetProcessWindowStation();
    [DllImport("user32.dll")] private static extern IntPtr GetThreadDesktop(uint thread);
    [DllImport("user32.dll",CharSet=CharSet.Unicode)] private static extern bool GetUserObjectInformation(IntPtr obj,int index,StringBuilder data,int size,out int needed);
    [DllImport("user32.dll",EntryPoint="GetUserObjectInformationW")] private static extern bool UserFlags(IntPtr obj,int index,out ObjectFlags flags,int size,out int needed);
    [DllImport("kernel32.dll")] private static extern uint GetCurrentThreadId();
    [DllImport("kernel32.dll")] private static extern uint WTSGetActiveConsoleSessionId();
    [StructLayout(LayoutKind.Sequential)] private struct ObjectFlags {public int inherit,reserved;public uint flags;}
    [StructLayout(LayoutKind.Sequential)] private struct GuiInfo {public uint size,flags;public IntPtr active,focus,capture,owner,move,caret;public Rect caretBounds;}
    private static string ObjectName(IntPtr obj){var name=new StringBuilder(128);int needed;return GetUserObjectInformation(obj,2,name,name.Capacity*2,out needed)?name.ToString():"unknown";}
    private static string Describe(IntPtr h){uint pid;GetWindowThreadProcessId(h,out pid);return string.Format("0x{0:x}/{1}/pid{2}",h.ToInt64(),Class(h),pid);}
    public static string DesktopInfo(IntPtr sink){
        uint process;uint thread=GetWindowThreadProcessId(sink,out process);
        var info=new GuiInfo{size=(uint)Marshal.SizeOf(typeof(GuiInfo))};bool got=GetGUIThreadInfo(thread,ref info);
        var station=GetProcessWindowStation();int needed;ObjectFlags flags;UserFlags(station,1,out flags,Marshal.SizeOf(typeof(ObjectFlags)),out needed);
        var windowClasses=new StringBuilder();
        EnumWindows((h,a)=>{uint pid;GetWindowThreadProcessId(h,out pid);if(pid==process || Class(h)=="#32768")windowClasses.AppendFormat(" [{0},visible={1}]",Describe(h),IsWindowVisible(h));return true;},IntPtr.Zero);
        var taskbar=FindWindowEx(IntPtr.Zero,IntPtr.Zero,"Shell_TrayWnd",null);Rect r;bool tray=TrayRect(sink,out r);
        return string.Format("session={0}, console={1}, station={2}, stationVisible={3}, desktop={4}, shell={5}, taskbar={6}/visible={7}, iconRect={8}, foreground={9}, GUIThread={10}/ok={11}/flags=0x{12:x}/active={13}/menuOwner={14}, nativeWindows:{15}",
            System.Diagnostics.Process.GetCurrentProcess().SessionId,WTSGetActiveConsoleSessionId(),ObjectName(station),(flags.flags&1)!=0,ObjectName(GetThreadDesktop(GetCurrentThreadId())),Describe(GetShellWindow()),Describe(taskbar),IsWindowVisible(taskbar),tray,Describe(GetForegroundWindow()),thread,got,info.flags,Describe(info.active),Describe(info.owner),windowClasses);
    }
    public static IntPtr TrayPopup(IntPtr sink){
        uint process;uint thread=GetWindowThreadProcessId(sink,out process);
        var gui=new GuiInfo{size=(uint)Marshal.SizeOf(typeof(GuiInfo))};GetGUIThreadInfo(thread,ref gui);
        IntPtr found=IntPtr.Zero;
        EnumWindows((h,a)=>{
            if(!IsWindowVisible(h) || Class(h)!="#32768")return true;
            uint pid;uint tid=GetWindowThreadProcessId(h,out pid);
            if(pid!=process && tid!=thread && (gui.owner!=sink || MenuLabel(h,0)!="打开 KeyDock Connect"))return true;
            found=h;return false;
        },IntPtr.Zero);
        return found;
    }
    [StructLayout(LayoutKind.Sequential)] private struct TrayItem {public uint size; public IntPtr window; public uint id; public Guid guid;}
    [StructLayout(LayoutKind.Sequential)] private struct Rect {public int left,top,right,bottom;}
    private static string Class(IntPtr h){var s=new StringBuilder(128);GetClassName(h,s,s.Capacity);return s.ToString();}
    public static string Text(IntPtr h){
        var s=new StringBuilder(8192);IntPtr result;
        return h!=IntPtr.Zero && ReadText(h,13,(IntPtr)s.Capacity,s,TimedFlags,750,out result)!=IntPtr.Zero?s.ToString():"";
    }
    private static string Caption(IntPtr h){var s=new StringBuilder(300);GetWindowText(h,s,s.Capacity);return s.ToString();}
    public static IntPtr Window(int process,string cls,string caption){
        IntPtr found=IntPtr.Zero;
        EnumWindows((h,a)=>{uint pid;GetWindowThreadProcessId(h,out pid);
            if(pid!=process || (!string.IsNullOrEmpty(cls) && Class(h)!=cls) || (!string.IsNullOrEmpty(caption) && Caption(h)!=caption)) return true;
            found=h;return false;},IntPtr.Zero);
        return found;
    }
    public static IntPtr Child(IntPtr parent,string kind,string caption){
        IntPtr found=IntPtr.Zero;
        EnumChildWindows(parent,(h,a)=>{var cls=Class(h).ToLowerInvariant();var style=GetStyle(h,-16);
            bool editable=cls=="edit";
            bool hits=kind=="button" ? cls=="button" && Text(h)==caption && IsWindowEnabled(h)
                : kind=="server" ? editable && (style & 36)==0
                : kind=="key" ? editable && (style & 32)!=0
                : kind=="checks" && editable && (style & 4)!=0;
            if(!hits)return true;
            found=h;return false;},IntPtr.Zero);
        return found;
    }
    public static void Enter(IntPtr h,string v){IntPtr result;if(h==IntPtr.Zero||WriteText(h,12,IntPtr.Zero,v,TimedFlags,2500,out result)==IntPtr.Zero||result==IntPtr.Zero)throw new Exception("Native edit control did not accept input");}
    public static void Click(IntPtr h){if(h==IntPtr.Zero)throw new Exception("Native button not available");IntPtr result;if(Send(h,0xF5,IntPtr.Zero,IntPtr.Zero,TimedFlags,4000,out result)==IntPtr.Zero)throw new TimeoutException("Native button did not respond");}
    public static void HideViaClose(IntPtr h){Post(h,0x10,IntPtr.Zero,IntPtr.Zero);}
    public static void SelectTray(IntPtr sink){if(sink==IntPtr.Zero)throw new Exception("No native tray callback window");Post(sink,0x403,IntPtr.Zero,(IntPtr)0x400);}
    private static bool TrayRect(IntPtr sink,out Rect box){
        var item=new TrayItem {size=(uint)Marshal.SizeOf(typeof(TrayItem)),guid=new Guid("ad3e9258-1960-428e-a59f-407e2d9469e3")};
        return Shell_NotifyIconGetRect(ref item,out box)==0 && box.right>box.left && box.bottom>box.top;
    }
    public static void OpenTrayMenu(IntPtr sink) {
        Rect box; int x,y;
        if(TrayRect(sink,out box)) { x=(box.left+box.right)/2; y=box.top-2; }
        else { x=GetSystemMetrics(0)-24; y=GetSystemMetrics(1)-48; }
        uint process;GetWindowThreadProcessId(sink,out process);AllowSetForegroundWindow(process);
        if(!Post(sink,0x403,(IntPtr)((x & 65535)|((y & 65535)<<16)),(IntPtr)0x7B))throw new Exception("Could not post shell context menu callback");
    }
    public static string MenuLabel(IntPtr pop,int index){var s=new StringBuilder(200);IntPtr menu;if(Send(pop,0x1e1,IntPtr.Zero,IntPtr.Zero,TimedFlags,1200,out menu)==IntPtr.Zero)throw new TimeoutException("Native tray menu did not respond");GetMenuString(menu,(uint)index,s,s.Capacity,0x400);return s.ToString();}
    public static void ExitViaMenu(IntPtr pop){Post(pop,0x100,(IntPtr)0x23,IntPtr.Zero); Post(pop,0x100,(IntPtr)0x0D,IntPtr.Zero);}
    // Some runner sessions expose a window surface but no usable desktop
    // framebuffer. Reject empty captures instead of publishing a black PNG.
    private static bool HasContent(Bitmap image,Rectangle area) {
        var seen=new bool[4096]; int colors=0,light=0,dark=255;
        for(int y=area.Top;y<area.Bottom;y+=2) for(int x=area.Left;x<area.Right;x+=2){
            var c=image.GetPixel(x,y); int l=(c.R*3+c.G*6+c.B)/10;
            light=Math.Max(light,l); dark=Math.Min(dark,l);
            int bin=(c.R/16)*256+(c.G/16)*16+c.B/16;
            if(!seen[bin]){seen[bin]=true;colors++;}
            if(light-dark>=48 && colors>=4) return true;
        }
        return false;
    }
    private static void BoundedCapture(Action capture){
        Exception failure=null;
        var thread=new System.Threading.Thread(new System.Threading.ThreadStart(delegate(){try{SetThreadDpiAwarenessContext((IntPtr)(-4));capture();}catch(Exception e){failure=e;}}));
        thread.IsBackground=true;thread.Start();
        if(!thread.Join(8000))throw new TimeoutException("Native screenshot timed out");
        if(failure!=null)throw new Exception("Native capture failed",failure);
    }
    public static void Capture(IntPtr hwnd,string path,string heading){BoundedCapture(delegate(){CaptureWindow(hwnd,path,heading);});}

    private static void SaveWindow(Bitmap image,string path,IntPtr hwnd,Rectangle frame,string heading,string source){
        image.Save(path,ImageFormat.Png);
        if(heading==null)return;
        IntPtr label=IntPtr.Zero;
        EnumChildWindows(hwnd,(h,a)=>{
            if(Class(h).Equals("static",StringComparison.OrdinalIgnoreCase) && IsWindowVisible(h) && Text(h)==heading){
                label=h;return false;
            }
            return true;
        },IntPtr.Zero);
        Rect inner,parent;
        if(label==IntPtr.Zero || !GetWindowRect(label,out inner) || !GetWindowRect(GetParent(label),out parent))
            throw new Exception("The visible heading STATIC was not found");
        var area=Rectangle.FromLTRB(inner.left-frame.Left,inner.top-frame.Top,inner.right-frame.Left,inner.bottom-frame.Top);
        var row=Rectangle.FromLTRB(parent.left-frame.Left,parent.top-frame.Top,parent.right-frame.Left,parent.bottom-frame.Top);
        var dpi=(int)GetDpiForWindow(label);
        int top=area.Bottom,bottom=area.Top-1;
        if(new Rectangle(Point.Empty,image.Size).Contains(area)){
            for(int y=area.Top;y<area.Bottom;y++)for(int x=area.Left;x<area.Right;x++){
                var c=image.GetPixel(x,y);
                if(c.R<140 && c.G<140 && c.B<140){top=Math.Min(top,y);bottom=Math.Max(bottom,y);break;}
            }
        }
        int ink=Math.Max(0,bottom-top+1);
        Console.WriteLine("Heading capture: {0}, DPI {1}, row {2}, STATIC {3}, visible ink y {4}..{5} ({6} px)",source,dpi,row,area,top,bottom,ink);
        if(dpi<=0 || area.Height*96<12*dpi || row.Height*96<26*dpi || !row.Contains(area) || ink*96<10*dpi){
            throw new Exception("The native title is clipped or its text was not painted in full");
        }
    }
    private static void CaptureWindow(IntPtr hwnd,string path,string heading=null){
        DwmFlush();
        Rect r;if(!GetWindowRect(hwnd,out r))throw new Exception("Cannot read native window bounds");
        var window=Rectangle.FromLTRB(r.left,r.top,r.right,r.bottom);
        var visible=window;
        Rect frame;
        // DWMWA_EXTENDED_FRAME_BOUNDS removes the invisible resize borders.
        if(DwmGetWindowAttribute(hwnd,9,out frame,Marshal.SizeOf(typeof(Rect)))==0){
            var bounds=Rectangle.FromLTRB(frame.left,frame.top,frame.right,frame.bottom);
            if(bounds.Width>0 && bounds.Height>0 && window.Contains(bounds))visible=bounds;
        }
        if(visible==window && (GetStyle(hwnd,-16)&0x40000)!=0){
            // Noncomposited sessions can report the full resize rect. Keep the
            // caption while trimming side and bottom frames to client bounds.
            Rect client;var origin=new Point();
            if(GetClientRect(hwnd,out client) && ClientToScreen(hwnd,ref origin)){
                var bounds=Rectangle.FromLTRB(origin.X,window.Top,origin.X+client.right,origin.Y+client.bottom);
                if(bounds.Width>0 && bounds.Height>0 && window.Contains(bounds))visible=bounds;
            }
        }
        if(GetForegroundWindow()==hwnd){
            try{
                using(var screen=new Bitmap(visible.Width,visible.Height,PixelFormat.Format24bppRgb)){
                    using(var g=Graphics.FromImage(screen))g.CopyFromScreen(visible.Location,Point.Empty,screen.Size);
                    if(HasContent(screen,new Rectangle(Point.Empty,screen.Size))){SaveWindow(screen,path,hwnd,visible,heading,"desktop");return;}
                }
            }catch(System.ComponentModel.Win32Exception){ /* Session may lack a readable desktop. */ }
        }
        // Render the window at its HWND origin, then retain just its visible frame.
        using(var surface=new Bitmap(window.Width,window.Height,PixelFormat.Format24bppRgb)){
            bool ok;
            using(var graphics=Graphics.FromImage(surface)){
                var dc=graphics.GetHdc();
                try {ok=PrintWindow(hwnd,dc,2);} finally {graphics.ReleaseHdc(dc);}
            }
            var crop=visible;crop.Offset(-window.Left,-window.Top);
            if(!ok || !HasContent(surface,crop))throw new Exception("Windows returned an empty native window capture");
            using(var result=surface.Clone(crop,PixelFormat.Format24bppRgb))SaveWindow(result,path,hwnd,visible,heading,"PrintWindow");
        }
    }
    public static void CaptureTray(IntPtr popup,IntPtr sink,string path){BoundedCapture(delegate(){CaptureTrayWindow(popup,sink,path);});}
    private static void CaptureTrayWindow(IntPtr popup,IntPtr sink,string path) {
        DwmFlush();
        Rect p;if(!GetWindowRect(popup,out p))throw new Exception("Cannot read the open tray menu");
        var menu=Rectangle.FromLTRB(p.left,p.top,p.right,p.bottom);
        var frame=menu; Rect icon;
        if(TrayRect(sink,out icon)) {
            var combined=Rectangle.Union(menu,Rectangle.FromLTRB(icon.left,icon.top,icon.right,icon.bottom));
            if(combined.Width<=menu.Width+180 && combined.Height<=menu.Height+120) frame=combined;
        }
        frame.Inflate(14,14);
        var desktop=new Rectangle(GetSystemMetrics(76),GetSystemMetrics(77),GetSystemMetrics(78),GetSystemMetrics(79));
        frame.Intersect(desktop);
        if(frame.Contains(menu)){
            try {
                using(var shot=new Bitmap(frame.Width,frame.Height,PixelFormat.Format24bppRgb)){
                    using(var g=Graphics.FromImage(shot))g.CopyFromScreen(frame.Left,frame.Top,0,0,shot.Size);
                    var inside=menu;inside.Offset(-frame.Left,-frame.Top);inside.Inflate(-3,-3);
                    if(HasContent(shot,inside)){shot.Save(path,ImageFormat.Png);return;}
                }
            } catch(System.ComponentModel.Win32Exception) { /* Try the actual popup HWND below. */ }
        }
        // No visible shell pixels in this session. Capture the native menu itself.
        CaptureWindow(popup,path);
    }
}
