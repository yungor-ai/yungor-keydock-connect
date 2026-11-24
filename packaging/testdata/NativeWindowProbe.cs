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
    [DllImport("user32.dll")] private static extern IntPtr SetThreadDpiAwarenessContext(IntPtr context);
    [DllImport("user32.dll")] private static extern bool PrintWindow(IntPtr hwnd,IntPtr hdc,uint flags);
    [DllImport("user32.dll",CharSet=CharSet.Unicode)] private static extern int GetMenuString(IntPtr menu,uint pos,StringBuilder text,int len,uint byPosition);
    [DllImport("user32.dll")] private static extern int GetSystemMetrics(int index);
    [DllImport("shell32.dll")] private static extern int Shell_NotifyIconGetRect(ref TrayItem id,out Rect box);
    [DllImport("dwmapi.dll")] private static extern int DwmFlush();
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
        var item=new TrayItem {size=(uint)Marshal.SizeOf(typeof(TrayItem)),window=sink,guid=new Guid("ad3e9258-1960-428e-a59f-407e2d9469e3")};
        return Shell_NotifyIconGetRect(ref item,out box)==0 && box.right>box.left && box.bottom>box.top;
    }
    public static void OpenTrayMenu(IntPtr sink) {
        Rect box; int x,y;
        if(TrayRect(sink,out box)) { x=(box.left+box.right)/2; y=box.top-2; }
        else { x=GetSystemMetrics(0)-24; y=GetSystemMetrics(1)-48; }
        Post(sink,0x403,(IntPtr)((x & 65535)|((y & 65535)<<16)),(IntPtr)0x7B);
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
    public static void Capture(IntPtr hwnd,string path){BoundedCapture(delegate(){CaptureWindow(hwnd,path);});}
    private static void CaptureWindow(IntPtr hwnd,string path){
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
                    if(HasContent(screen,new Rectangle(Point.Empty,screen.Size))){screen.Save(path,ImageFormat.Png);return;}
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
            using(var result=surface.Clone(crop,PixelFormat.Format24bppRgb))result.Save(path,ImageFormat.Png);
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
