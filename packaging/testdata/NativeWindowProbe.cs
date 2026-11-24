using System;
using System.Text;
using System.Diagnostics;
using System.Drawing;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;

public static class KeyDockWindowProbe {
    private delegate bool EnumProc(IntPtr hwnd, IntPtr arg);
    [DllImport("user32.dll")] private static extern bool EnumWindows(EnumProc cb, IntPtr unused);
    [DllImport("user32.dll")] private static extern bool EnumChildWindows(IntPtr parent, EnumProc cb, IntPtr unused);
    [DllImport("user32.dll")] private static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint process);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] private static extern int GetClassName(IntPtr hwnd,StringBuilder str,int size);
    [DllImport("user32.dll", CharSet=CharSet.Unicode, EntryPoint="SendMessageW")] private static extern IntPtr ReadText(IntPtr hwnd,uint msg,IntPtr size,StringBuilder text);
    [DllImport("user32.dll", CharSet=CharSet.Unicode, EntryPoint="SendMessageW")] private static extern IntPtr WriteText(IntPtr hwnd,uint msg,IntPtr size,string text);
    [DllImport("user32.dll", EntryPoint="SendMessageW")] private static extern IntPtr Send(IntPtr hwnd,uint msg,IntPtr w,IntPtr l);
    [DllImport("user32.dll", EntryPoint="PostMessageW")] private static extern bool Post(IntPtr hwnd,uint msg,IntPtr w,IntPtr l);
    [DllImport("user32.dll", EntryPoint="GetWindowLongW")] private static extern int GetStyle(IntPtr hwnd,int index);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hwnd);
    [DllImport("user32.dll")] private static extern bool IsWindowEnabled(IntPtr hwnd);
    [DllImport("user32.dll")] private static extern bool GetWindowRect(IntPtr hwnd,out Rect rect);
    [DllImport("user32.dll")] private static extern bool PrintWindow(IntPtr hwnd,IntPtr hdc,uint flags);
    [DllImport("user32.dll",CharSet=CharSet.Unicode)] private static extern int GetMenuString(IntPtr menu,uint pos,StringBuilder text,int len,uint byPosition);
    [StructLayout(LayoutKind.Sequential)] private struct Rect {public int left,top,right,bottom;}
    private static string Class(IntPtr h){var s=new StringBuilder(128);GetClassName(h,s,s.Capacity);return s.ToString();}
    public static string Text(IntPtr h){var s=new StringBuilder(8192);ReadText(h,13,(IntPtr)s.Capacity,s);return s.ToString();}
    public static IntPtr Window(int process,string cls,string caption){
        IntPtr found=IntPtr.Zero;
        EnumWindows((h,a)=>{uint pid;GetWindowThreadProcessId(h,out pid);
            if(pid!=process || (cls!=null && Class(h)!=cls) || (caption!=null && Text(h)!=caption)) return true;
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
    public static void Enter(IntPtr h,string v){if(h==IntPtr.Zero||WriteText(h,12,IntPtr.Zero,v)==IntPtr.Zero)throw new Exception("Native edit control did not accept input");}
    public static void Click(IntPtr h){if(h==IntPtr.Zero)throw new Exception("Native button not available");Send(h,0xF5,IntPtr.Zero,IntPtr.Zero);}
    public static void HideViaClose(IntPtr h){Post(h,0x10,IntPtr.Zero,IntPtr.Zero);}
    public static void SelectTray(IntPtr sink){if(sink==IntPtr.Zero)throw new Exception("No native tray callback window");Post(sink,0x403,IntPtr.Zero,(IntPtr)0x400);}
    public static void OpenTrayMenu(IntPtr sink){Post(sink,0x403,(IntPtr)0x100010,(IntPtr)0x7B);}
    public static string MenuLabel(IntPtr pop,int index){var s=new StringBuilder(200);var menu=Send(pop,0x1e1,IntPtr.Zero,IntPtr.Zero);GetMenuString(menu,(uint)index,s,s.Capacity,0x400);return s.ToString();}
    public static void ExitViaMenu(IntPtr pop){Post(pop,0x100,(IntPtr)0x23,IntPtr.Zero); Post(pop,0x100,(IntPtr)0x0D,IntPtr.Zero);}
    public static void Capture(IntPtr hwnd,string path){
        Rect r;if(!GetWindowRect(hwnd,out r))throw new Exception("Cannot read window bounds");
        using(var img=new Bitmap(r.right-r.left,r.bottom-r.top,PixelFormat.Format32bppArgb)){
            using(var graphics=Graphics.FromImage(img)){
                var dc=graphics.GetHdc();bool ok;
                try{ok=PrintWindow(hwnd,dc,2);}finally{graphics.ReleaseHdc(dc);}
                if(!ok)graphics.CopyFromScreen(r.left,r.top,0,0,img.Size);
            }
            img.Save(path,ImageFormat.Png);
        }
    }
}
