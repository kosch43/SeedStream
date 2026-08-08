import {
  LayoutDashboard, Settings, LogOut,
  Sun, Moon, Monitor, Zap, FileText, User, MoreVertical, ChartColumn, AlertTriangle
} from "lucide-react"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarGroupContent,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { ShieldCheck } from "lucide-react"
import { cn } from "@/lib/utils"

const navMain = [
  { id: "dashboard", title: "Dashboard", icon: LayoutDashboard },
  { id: "statistics", title: "Statistics", icon: ChartColumn },
  { id: "cerberus", title: "Cerberus", icon: ShieldCheck },
  { id: "logs", title: "Logs", icon: FileText },
  { id: "install", title: "Streams", icon: Zap },
  { id: "settings", title: "Settings", icon: Settings },
]


export function AppSidebar({
  activePage,
  onNavigate,
  version,
  currentUser,
  forcePasswordResetEnabled,
  onLogout,
  theme,
  onThemeChange,
  ...props
}) {
  return (
    <Sidebar collapsible="offcanvas" {...props}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              size="lg"
              onClick={() => onNavigate("dashboard")}
              className="h-auto py-3"
            >
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground">
                <Zap className="size-6" />
              </div>
              <div className="grid min-w-0 flex-1 gap-0.5 text-left">
                <span className="truncate text-lg font-semibold leading-none">SeedStream</span>
                {version && (
                  <span className="truncate text-xs text-muted-foreground leading-none">v{version}</span>
                )}
              </div>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        {/* Main nav */}
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {navMain.map((item) => (
                <SidebarMenuItem key={item.id}>
                  <SidebarMenuButton
                    isActive={activePage === item.id}
                    tooltip={item.title}
                    onClick={() => onNavigate(item.id)}
                  >
                    <item.icon />
                    <span>{item.title}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}

            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        {/* Theme selector - pushed to bottom */}
        <SidebarGroup className="mt-auto">
          <SidebarGroupContent>
            <SidebarMenu>
              <SidebarMenuItem>
                <ToggleGroup
                  type="single"
                  value={theme}
                  onValueChange={(v) => v && onThemeChange(v)}
                  className="justify-center"
                >
                  <ToggleGroupItem value="light" size="sm" aria-label="Light">
                    <Sun className="size-4" />
                  </ToggleGroupItem>
                  <ToggleGroupItem value="dark" size="sm" aria-label="Dark">
                    <Moon className="size-4" />
                  </ToggleGroupItem>
                  <ToggleGroupItem value="system" size="sm" aria-label="System">
                    <Monitor className="size-4" />
                  </ToggleGroupItem>
                </ToggleGroup>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter>
        {currentUser && currentUser !== 'legacy' && (
          <div className="mt-2 pt-2 border-t border-sidebar-border">
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  className={cn(
                    "flex w-full items-center gap-3 rounded-lg p-2 text-left outline-none",
                    "hover:bg-sidebar-accent focus:bg-sidebar-accent focus-visible:ring-2 focus-visible:ring-ring"
                  )}
                  aria-label="Open account menu"
                >
                  <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground">
                    <Zap className="size-4" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <span className="truncate block text-sm font-medium flex items-center gap-1.5">
                      <span className="truncate">{currentUser}</span>
                      {forcePasswordResetEnabled && (
                        <TooltipProvider delayDuration={100}>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span
                                className="inline-flex items-center text-amber-600"
                                aria-label="Forced password reset is enabled via environment variable"
                              >
                                <AlertTriangle className="size-3.5 shrink-0" />
                              </span>
                            </TooltipTrigger>
                            <TooltipContent side="top" align="start">
                              Forced password reset is enabled via env var. Disable it after the password is changed.
                            </TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      )}
                    </span>
                    <span className="truncate block text-xs text-muted-foreground">Account</span>
                  </div>
                  <MoreVertical className="size-4 shrink-0 text-muted-foreground" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent side="top" align="end" className="w-48">
                <DropdownMenuItem onClick={() => onNavigate("profile")}>
                  <User className="mr-2 h-4 w-4" />
                  Profile
                </DropdownMenuItem>
                <DropdownMenuItem onClick={onLogout}>
                  <LogOut className="mr-2 h-4 w-4" />
                  Log out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        )}
      </SidebarFooter>
    </Sidebar>
  )
}
